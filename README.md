# terminal-connect
Placeholder repository for everything that is needed for Terminal Connect.

## Run the example WASM application

### Prerequisites

1. Install and run `aperture` from the
   [`mailbox` branch of the internal repo](https://github.com/lightninglabs/aperture-mailbox/tree/mailbox).
   We assume `aperture` is running with TLS on `localhost:11110`.

### Run lnd

1. Compile and run `lnd` from the [`mailbox` branch of the internal repo](https://github.com/lightninglabs/lnd-mailbox/tree/mailbox).
2. Run `lnd connectmailbox localhost:11110 --dev_server`, copy the password for
   later use. You'll need to run this command again to generate a new password
   for each new connection attempt. At the moment re-using the same password
   across multiple runs of the WASM client doesn't work cleanly.

### Compile and run the example

1. In this repo, run `make wasm` to compile the example client to WASM.
2. Run `make example-server` to start the example HTTP server.
3. Open [https://localhost:11110](https://localhost:11110) and make sure you
   add an exception for `aperture`'s TLS certificate! This might not work in
   Chrome. This step is crucial, without it the connection from the example app
   to `aperture` will fail.
3. Visit [http://localhost:8080](http://localhost:8080) to see the example
   client in action. It is recommended to open the browser console (F12) to see
   the console logs.
