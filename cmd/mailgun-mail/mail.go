// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Mailgun-mail, a drop-in replacement for the sending half
// of the standard Unix mail program, sends mail using Mailgun.
//
// Usage:
//
//	mailgun-mail [-Edntv] [-a file] [-b bcc] [-c cc] [-r from] [-s subject] to...
//
// Mailgun-mail sends mail to the given "to" addresses.
//
// The options are a subset of the standard mail program's options:
//
//	-E  discard (do not send) empty messages
//	-d  print debugging information
//	-n  do not send any mail
//	-t  use Subject:, To:, Cc:, Bcc: lines from input
//	-v  verbose mode
//
//	-a file
//	    attach the file to the message (can repeat)
//	-b addr
//	    bcc the address (can repeat)
//	-c addr
//	    cc the address (can repeat)
//	-r from
//	    set the from address
//	-s subject
//	    set mail subject
//
// Configuration
//
// Mailgun-mail expects to find an mailgun API domain and authorization key
// of the form "<domain> api:key-<hexstring>" in the environment variable
// $MAILGUNKEY, or else in the file $HOME/.mailgun.key,
// or else in the file /etc/mailgun.key.
//
// Diagnostics
//
// If the file /var/log/mailgun.log can be opened for writing, mailgun
// logs its actions, successes, and failures there.
//
package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"net/mail"
	"os"
	"strings"

	"rsc.io/getopt"
	"rsc.io/mailgun/cmd/internal/mg"
)

func usage() {
	mg.Logf("invalid command line")
	fmt.Fprintf(os.Stderr, "usage: mailgun-mail [options] [addr...]\n")
	getopt.PrintDefaults()
	os.Exit(2)
}

var (
	Eflag bool
	dflag bool
	nflag bool
	tflag bool
	vflag bool
)

func main() {
	mg.Init()

	var to, cc, bcc mg.AddrListFlag
	var aflag, rflag, sflag mg.StringListFlag
	var body bytes.Buffer

	flag.BoolVar(&Eflag, "E", false, "discard (do not send) empty messages")
	flag.BoolVar(&dflag, "d", false, "print debugging information")
	flag.BoolVar(&nflag, "n", false, "do not send actual mail")
	flag.BoolVar(&tflag, "t", false, "read To:, CC:, and BCC: lines from message header")
	flag.BoolVar(&vflag, "v", false, "verbose mode")

	flag.Var(&aflag, "a", "attach `file` to message")
	flag.Var(&bcc, "b", "BCC `address")
	flag.Var(&cc, "c", "CC `address")
	flag.Var(&rflag, "r", "send mail from `address`") // list so we can tell empty from missing
	flag.Var(&sflag, "s", "set message `subject`")    // list so we can tell empty from missing

	flag.Usage = usage
	getopt.Parse()
	mg.DisableMail = nflag
	mg.DebugHTTP = dflag
	mg.Verbose = vflag

	// To addresses from command line.
	if flag.NArg() == 0 && !tflag {
		mg.Die(fmt.Errorf("mail reading is not supported"))
	}
	for _, arg := range flag.Args() {
		if err := to.Set(arg); err != nil {
			mg.Die(fmt.Errorf("cannot parse To: address: %v", err))
		}
	}

	// From address.
	from := new(mail.Address)
	if len(rflag) > 0 {
		a, err := mg.ParseAddress(rflag[len(rflag)-1])
		if err != nil {
			mg.Die(fmt.Errorf("cannot parse From: address: %v", err))
		}
		from = a
	} else {
		from.Address = os.Getenv("USER")
		if from.Address == "" {
			mg.Die(fmt.Errorf("cannot determine From address: -r not used, and $USER not set"))
		}
	}

	// Subject from command line or TTY.
	b := bufio.NewReader(os.Stdin)
	subject := ""
	if len(sflag) > 0 {
		subject = sflag[len(sflag)-1]
	} else if mg.IsTTY {
		fmt.Fprintf(os.Stderr, "Subject: ")
		line, err := b.ReadBytes('\n')
		if len(line) == 0 && err == io.EOF {
			mg.Logf("no subject, no text, not sending")
			return
		}
		if len(line) == 0 && err != nil {
			mg.Die(fmt.Errorf("reading subject: %v", err))
		}
		subject = strings.TrimSuffix(string(line), "\n")
	}

	// Subject, To, CC, BCC from input using -t.
	if tflag {
		for {
			line, err := b.ReadBytes('\n')
			if len(line) == 0 && err == io.EOF {
				break
			}
			if len(line) == 0 && err != nil {
				mg.Die(fmt.Errorf("reading message: %v", err))
			}
			i := bytes.IndexByte(line, ':')
			if i < 0 {
				if len(line) > 0 && line[0] != '\n' {
					if line[0] == '.' && (len(line) == 1 || len(line) == 2 && line[1] == '\n') && mg.IsTTY {
						goto Send
					}
					body.Write(line)
				}
				break
			}
			key := string(line[:i])
			val := strings.TrimSpace(string(line[i+1:]))
			var list *mg.AddrListFlag
			switch strings.ToLower(key) {
			default:
				fmt.Fprintf(os.Stderr, "mailgun-mail: ignoring header field %q\n", strings.TrimSuffix(string(line), "\n"))
			case "subject":
				subject = val
				continue
			case "to":
				list = &to
				key = "To"
			case "cc":
				list = &cc
				key = "CC"
			case "bcc":
				list = &bcc
				key = "BCC"
			}

			addrs, err := mail.ParseAddressList(val)
			if err != nil {
				mg.Die(fmt.Errorf("cannot parse %s: list: %v", key, err))
			}
			*list = append(*list, addrs...)
		}
	}

	if mg.IsTTY {
		// Message from TTY ends with . on line by itself or EOF.
		for {
			line, err := b.ReadBytes('\n')
			if len(line) == 0 && err == io.EOF {
				break
			}
			if len(line) == 0 && err != nil {
				mg.Die(fmt.Errorf("reading message: %v", err))
			}
			if len(line) == 2 && line[0] == '.' && line[1] == '\n' {
				break
			}
			body.Write(line)
		}
		fmt.Fprintf(os.Stderr, "EOT\n") // dumb but mail does it
	} else {
		// Message from stdin is until EOF.
		_, err := io.Copy(&body, b)
		if err != nil {
			mg.Die(fmt.Errorf("reading message: %v", err))
		}
	}

Send:
	msg := &mg.Message{
		From:        from,
		To:          to,
		CC:          cc,
		BCC:         bcc,
		Subject:     subject,
		Body:        body.String(),
		Attachments: aflag,
	}
	if vflag {
		printList := func(x []*mail.Address) string {
			var s []string
			for _, a := range x {
				s = append(s, a.String())
			}
			return strings.Join(s, ", ")
		}
		fmt.Fprintf(os.Stderr, "from: %v\n", msg.From)
		fmt.Fprintf(os.Stderr, "to: %v\n", printList(msg.To))
		fmt.Fprintf(os.Stderr, "cc: %v\n", printList(msg.CC))
		fmt.Fprintf(os.Stderr, "bcc: %v\n", printList(msg.BCC))
		fmt.Fprintf(os.Stderr, "subject: %v\n", msg.Subject)
		fmt.Fprintf(os.Stderr, "body: %d bytes\n", len(msg.Body))
		if len(msg.Attachments) > 0 {
			fmt.Fprintf(os.Stderr, "attachments:\n")
			for _, a := range msg.Attachments {
				fmt.Fprintf(os.Stderr, "\t%s\n", a)
			}
		}
	}
	mg.Mail(msg)
}
