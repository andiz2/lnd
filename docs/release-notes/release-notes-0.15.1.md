# Release Notes

## Build system

* [Add the release build directory to the `.gitignore` file to avoid the release
  binary digest to be different whether that folder exists or
  not](https://github.com/lightningnetwork/lnd/pull/6676)

## `lncli`

* [Add `payment_addr` flag to `buildroute`](https://github.com/lightningnetwork/lnd/pull/6576)
  so that the mpp record of the route can be set correctly.

## Documentation

* [Add minor comment](https://github.com/lightningnetwork/lnd/pull/6559) on
  subscribe/cancel/lookup invoice parameter encoding.
  
## RPC Server

* [Add previous_outpoints to listchaintxns](https://github.com/lightningnetwork/lnd/pull/6321)
* [Fix P2TR support in
  `ComputeInputScript`](https://github.com/lightningnetwork/lnd/pull/6680).

## Bug Fixes

* Fixed data race found in
  [`TestSerializeHTLCEntries`](https://github.com/lightningnetwork/lnd/pull/6673).

* [Fixed a bug in the `SignPsbt` RPC that produced an invalid response when
  signing a NP2WKH input](https://github.com/lightningnetwork/lnd/pull/6687).

## RPC Server

* [Add wallet reserve rpc & field in wallet balance](https://github.com/lightningnetwork/lnd/pull/6592)

## Bug Fixes

* [Update the urfave/cli package](https://github.com/lightningnetwork/lnd/pull/6682) because
  of a flag parsing bug.

* [DisconnectPeer no longer interferes with the peerTerminationWatcher. This previously caused
  force closes.](https://github.com/lightningnetwork/lnd/pull/6655)

# Contributors (Alphabetical Order)

* Elle Mouton
* ErikEk
* Eugene Siegel
* Oliver Gugger
* Priyansh Rastogi
* Yong Yu