# rsc.io/mailgun

This repo holds basic utilities for interacting with the
[Mailgun email service](https://www.mailgun.com).

`rsc.io/mailgun/cmd/mailgun-mail` is a drop-in replacement for the mail-sending mode of BSD mailx.

`rsc.io/mailgun/cmd/mailgun-sendmail` is a drop-in replacement for the mail-sending mode of the sendmail daemon.

The idea behind both these programs is that you can install them in
place of the usual mail and sendmail programs, and then programs can 
still send mail from your local system, without having to configure and
run a full-blown mail system.

