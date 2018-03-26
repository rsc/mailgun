// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package mg holds common code shared between
// the various mailgun commands.
package mg

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/httputil"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
)

var (
	IsTTY   bool = isTTY()
	Domain  string
	APIKey  string
	User    string = os.Getenv("USER")
	Verbose bool
)

func isTTY() bool {
	stdin, err := os.Stdin.Stat()
	return err == nil && stdin.Mode()&(os.ModeDevice|os.ModeCharDevice) == os.ModeDevice|os.ModeCharDevice
}

func Init() {
	f, err := os.OpenFile("/var/log/mailgun.log", os.O_WRONLY|os.O_APPEND, 0)
	if err == nil {
		log.SetOutput(f)
	} else {
		log.SetOutput(ioutil.Discard)
	}

	readConfig()
}

func readConfig() {
	key := os.Getenv("MAILGUNKEY")
	if key != "" {
		parseKey("$MAILGUNKEY", key)
		return
	}
	file := os.Getenv("HOME") + "/.mailgun.key"
	data, err := ioutil.ReadFile(file)
	if err == nil {
		parseKey(file, string(data))
		return
	}
	file = "/etc/mailgun.key"
	data, err = ioutil.ReadFile(file)
	if err == nil {
		parseKey(file, string(data))
		return
	}
	Die(err)
}

func Die(err error) {
	log.Printf("[%s]%q %v", User, os.Args, err)
	fmt.Fprintf(os.Stderr, "%s: %s\n", filepath.Base(os.Args[0]), err)
	os.Exit(2)
}

func parseKey(src, key string) {
	f := strings.Fields(key)
	if len(f) != 2 || !strings.Contains(f[0], ".") || !strings.HasPrefix(f[1], "api:key-") {
		Die(fmt.Errorf("malformed mailgun API key in %s", src))
	}
	Domain = f[0]
	APIKey = strings.TrimPrefix(f[1], "api:")
}

func ParseAddress(addr string) (*mail.Address, error) {
	if !strings.ContainsAny(addr, "<>()\" \t\r\n") {
		return &mail.Address{Address: addr}, nil
	}
	if strings.HasSuffix(addr, ">") && !strings.HasPrefix(addr, `"`) {
		// Foo (Bar) <baz@quux.com>
		// not handled "correctly" by ParseAddress.
		if i := strings.LastIndex(addr, "<"); i >= 0 {
			return &mail.Address{Name: strings.TrimSpace(addr[:i]), Address: addr[i:len(addr)-1]}, nil
		}
	}
	return mail.ParseAddress(addr)

}

type AddrListFlag []*mail.Address

func (x *AddrListFlag) String() string {
	if len(*x) == 0 {
		return ""
	}
	return "[addrs]"
}

func (x *AddrListFlag) Set(addr string) error {
	a, err := ParseAddress(addr)
	if err != nil {
		return err
	}
	*x = append(*x, a)
	return nil
}

type StringListFlag []string

func (x *StringListFlag) String() string {
	if len(*x) == 0 {
		return ""
	}
	return "[strings]"
}

func (x *StringListFlag) Set(s string) error {
	*x = append(*x, s)
	return nil
}

var (
	DebugHTTP   bool
	DisableMail bool
)

// A Message is a structured mail message to be sent.
type Message struct {
	From        *mail.Address
	To          []*mail.Address
	CC          []*mail.Address
	BCC         []*mail.Address
	Subject     string
	Body        string   `json:"-"`
	Attachments []string // file names
}

// Allow implicit local domain in addresses.
func FixLocalAddr(a *mail.Address) {
	if !strings.Contains(a.Address, "@") {
		a.Address += "@" + Domain
	}
}

func FixLocalAddrs(list []*mail.Address) {
	for _, a := range list {
		FixLocalAddr(a)
	}
}

func Mail(msg *Message) {
	FixLocalAddr(msg.From)
	FixLocalAddrs(msg.To)
	FixLocalAddrs(msg.CC)
	FixLocalAddrs(msg.BCC)

	var allTo []*mail.Address
	allTo = append(allTo, msg.To...)
	allTo = append(allTo, msg.CC...)
	allTo = append(allTo, msg.BCC...)

	w, end := startPost(msg.From, allTo, "messages")
	check(w.WriteField("from", msg.From.String()))
	for _, a := range msg.To {
		check(w.WriteField("to", a.String()))
	}
	for _, a := range msg.CC {
		check(w.WriteField("cc", a.String()))
	}
	for _, a := range msg.BCC {
		check(w.WriteField("bcc", a.String()))
	}
	if msg.Subject != "" {
		check(w.WriteField("subject", msg.Subject))
	}
	check(w.WriteField("text", msg.Body))

	for _, file := range msg.Attachments {
		ww, err := w.CreateFormFile("attachment", filepath.Base(file))
		check(err)
		f, err := os.Open(file)
		if err != nil {
			Die(fmt.Errorf("attaching file: %v", err))
		}
		if _, err := io.Copy(ww, f); err != nil {
			Die(fmt.Errorf("attaching file: %v", err))
		}
		f.Close()
	}
	check(w.Close())
	end()
}

func startPost(from *mail.Address, to []*mail.Address, endpoint string) (w *multipart.Writer, end func()) {
	pr, pw := io.Pipe()
	w = multipart.NewWriter(pw)
	endpoint = "https://api.mailgun.net/v3/" + Domain + "/" + endpoint
	c := make(chan int)
	go runPost(from, to, endpoint, w.FormDataContentType(), pr, c)
	end = func() {
		pw.Close()
		<-c
	}
	return w, end
}

type countingReader struct {
	total int64
	r     io.Reader
}

func (c *countingReader) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	c.total += int64(n)
	return n, err
}

func runPost(from *mail.Address, to []*mail.Address, endpoint, bodytype string, body io.Reader, c chan int) {
	cr := &countingReader{r: body}
	req, err := http.NewRequest("POST", endpoint, cr)
	check(err)
	req.Header.Set("Content-Type", bodytype)
	req.SetBasicAuth("api", APIKey)

	if DebugHTTP {
		dump, err := httputil.DumpRequest(req, true)
		if err != nil {
			Die(fmt.Errorf("dumping request: %v", err))
		}
		os.Stderr.Write(dump)
	}

	if DisableMail {
		fmt.Fprintf(os.Stderr, "not sending mail (disabled)\n")
		io.Copy(ioutil.Discard, body)
		c <- 1
		return
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		Die(fmt.Errorf("sending mail: %v", err))
	}

	if DebugHTTP {
		dump, err := httputil.DumpResponse(resp, true)
		if err != nil {
			Die(fmt.Errorf("dumping response: %v", err))
		}
		os.Stderr.Write(dump)
	}

	data, err := ioutil.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		Die(fmt.Errorf("sending mail: %v\n%s", resp.Status, data))
	}
	if err != nil {
		Die(fmt.Errorf("sending mail: %v\n%s", err, data))
	}

	var mailResp struct {
		Message string `json:"message"`
		ID      string `json:"id"`
	}
	if err := json.Unmarshal(data, &mailResp); err != nil {
		Die(fmt.Errorf("sending mail: invalid JSON response: %v\n%s", err, data))
	}
	var compact bytes.Buffer
	json.Compact(&compact, data)
	log.Printf("[%s]%q from=%q to=%q len=%d resp=%s", User, os.Args, from, to, cr.total, compact.Bytes())
	if IsTTY || Verbose {
		fmt.Fprintf(os.Stderr, "mailgun: %s\n", mailResp.Message)
	}
	c <- 1
}
func check(err error) {
	if err != nil {
		Die(fmt.Errorf("creating mailgun API request: %v", err))
	}
}

func MailMIME(from *mail.Address, to []*mail.Address, mime io.Reader) {
	FixLocalAddr(from)
	FixLocalAddrs(to)

	w, end := startPost(from, to, "messages.mime")
	for _, a := range to {
		check(w.WriteField("to", a.String()))
	}
	ww, err := w.CreateFormFile("message", "mime.msg")
	check(err)
	_, err = io.Copy(ww, mime)
	check(err)
	check(w.Close())
	end()
}

func Logf(format string, args ...interface{}) {
	log.Printf("[%s]%q %s", User, os.Args, fmt.Sprintf(format, args...))
}
