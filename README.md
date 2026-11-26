# corekiosk

corekiosk is a bunch of toolkit and libraries for deploying applications or brand activation installation in a kiosk-manner. 

## ckframework
ckframework is a go library. ckframework is where you define the game loop/kiosk loop, what screens exists, and what fields/form exists, and what kind of configuration exists in your kiosk/game app. 
ckframework is used downstream with ckserver, cklauncher, and is what the ckclient will follow.

## ckserver
ckserver is a library for the server logic of the kiosk. It runs Gin and coder/websocket. It has fixed endpoints for pairing, data-syncing, and established websocket datastructures.

## ckclient-rn


## cklauncher
A packager and a launcher in the Desktop Windows/Linux (but forget Linux support for now just focus on Windows) for corekiosk apps. It has the capability to change the settings before an app is launched and read the settings. It also has integration with cktelemetry so it can send telemetry information before the app is launch. It can install itself as a service so when system crash or restarts it autostarts nicely with a 30 second countdown till start. If game/app crashes it will autorestart unless cancelled.

Written in Go with https://github.com/golang-gui/goui

cklauncher is not used like an external tool, but as a library called from the server logic that uses ckserver. This way, cklauncher holds the reference to the ckserver object that's going to be created, and "packaging" is the process of sidecaring the game binary/kiosk binary itself. cklauncher have the capability of launching the server before the game binary, and just passing the server url as an argument to the game binary.

## cktelemetry-server


## cktelemetry-rn

