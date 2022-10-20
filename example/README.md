# LNC Example HTML Page

This folder contains an example HTML page with JS code used to demonstrate how
to connect to a `litd` node using LNC on the web.

## Setup

Follow these steps to be able to view the example page in a browser on your
machine.

1. Clone this repository
   ```
   $ git clone git@github.com:lightninglabs/lightning-node-connect.git
   $ cd lightning-node-connect
   ```
1. Build the WASM binary
   ```
   $ make wasm
   ```
1. Run the HTTP server which will make the HTML page and WASM binary accessible
   to your web browser
   ```
   $ make example-server
   ```
1. Open [http://localhost:8080](http://localhost:8080) in a web browser to view
   the page

## Using the Example Page

Here's how to use the HTML page to connect to your `litd` node

1. Enter a namespace or use the default value. This value is used to group the
   data that is stored in your browser's `localStorage`. By using different
   namespaces, you can maintain connection information for multiple nodes in
   one browser. However, you can only connect to one node at a time.
1. Click on the "Initiate the client" button.
   1. If you had previously connected using the specified namespace, it will
      attempt to automatically reconnect to your existing session.
   1. If you have not connected using the specified namespace, you will be
      prompted to enter your Pairing Phrase.
1. Enter the Pairing Phrase that can be copied from your `litd` node's web UI.
1. Keep the default mailbox server unless you used a custom server when
   creating the session in `litd`.
1. Click on the "Connect to server" button to establish the LNC connection.
1. A section of labels and buttons will be displayed allowing you to call
   many of the RPCs exposed by LND, Loop, Pool, and Faraday.
1. Click on the first "GetInfo" button. The RPC response will be displayed
   above the LND label.
