// +build js

package main

type config struct {
	MailboxServer    string `long:"mailboxserver" description:"Use the mailbox server with the given address to connect to lnd"`
	MailboxPassword  string `long:"mailboxpassword" description:"The password for connecting to the mailbox"`
	MailboxDevServer bool   `long:"mailboxdevserver" description:"The mailbox server is a development server, turn off TLS validation"`
	DebugLevel       string `long:"debuglevel" description:"Logging level for all subsystems {trace, debug, info, warn, error, critical} -- You may also specify <subsystem>=<level>,<subsystem2>=<level>,... to set the log level for individual subsystems -- Use show to list available subsystems"`
}
