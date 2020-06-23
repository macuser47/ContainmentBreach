# Containment Breach Hole Punching Server

Containment Breach is a service designed for setting up UDP p2p sessions via 
[hole punching](https://en.wikipedia.org/wiki/UDP_hole_punching). The basic idea is that
2 peers that want to connect to each other connect to _Containment breach_, which informs
each peer of the UDP port of the other peer, which they can use to set up a connection.

Containment Breach works on both Windows and Linux.

Containment Breach supports 2 different type of connection setup:

* Peer mode, which allows 2 peers that know each other's IP addresses to connect
* Client/Server mode, which allows a Server to register itself with _Containment breach_
  and be notified via a TCP connection whenever a client tries to connect. In this model
  the Client needs to know the Server IP, but the Server does not need to know the Client IPs
  in advance.

## Usage overview
In order to set up a connection, each peer needs to send a UDP packet to the server, which 
the server uses to get the UDP port number of the peer. Additionally, the peer needs to send
a _peer port_ with the UDP packet, which uniquely identifies the peer if it's on LAN with
other peers. Think of this is the P2P version of a port number.

Once the UDP packet has been sent, the peer is expected to initiate a TCP connection to the server
to wait for its peer/clients/server to connect. The peer sends a request containing identifying information,
and the server will respond with peer information when it becomes available.

There are some slight differences in the TCP connection depending on the connection type.
In peer mode, the UDP+TCP transaction can happen in any order between the 2 peers. The peer that 
completes its UDP+TCP handshake first will wait for the other peer before receiving a response.

In Server/Client mode, the peer server opens a persistent TCP connection with the containment breach
server and receives packets every time a client connects. If a client tries to connect to a host 
that doesn't exist, it will receive an error instead of waiting.

Connection leases time out after 90 seconds by default. Any peers waiting for a connection receive
a timeout error once a lease has expired.

## Installation

Run
```
sudo make install
```
To install `containment_breach` to /usr/local/bin/.

### Usage:
```
containment_breach [-port=PORT] [-lease_timeout=TIMEOUT]
Usage of containment_breach:
  -lease_timeout int
        PeerPort lease timeout (default 90)
  -port int
        Server UDP/TCP port (default 6969)
```


## API Definition
Containment Breach uses a JSON api to communicate with peers.

UDP requests are formatted as follows:
```json
{
	"PeerPort": int
}
```
Where `PeerPort` is the "P2P port number" associated with the peer. Make sure this is unique for peers on the same LAN.
Note that PeerPorts are not network port numbers. They're just used to identify P2P connections.

TCP requests are formatted as follows:
```json
{
	"RemoteIP":       string,
	"RemotePeerPort": int,
	"LocalPeerPort":  int,
	"ConnectionType": string
}
```
Where
* `RemoteIP` is the IP of the remote peer we're trying to connect to. This option is ignored for the `Server` connection type.
* `RemotePeerPort` is the PeerPort of the remote peer we're trying to connect to. This option is ignored for the `Server` connection type.
* `LocalPeerPort` is the PeerPort of the peer sending the request. This is used for associating the UDP packet just sent with this TCP session.
* `ConnectionType` is `"Server"`,  `"Client"`, `"Peer"`, or `"KeepAlive"`, depeiding on the intended connection type. `"KeepAlive"` is
  used for `Server` connections to maintin the TCP session.

TCP responses are formatted as follows:
```json
{
	"SourcePort": int,
	"SourceIP":   string,
	"DestPort":   int,
	"DestIP":     string,
	"Error":      string
}
```
Where
* `SourcePort` is the UDP port of the source.
* `SourceIP` is the IP of the source.
* `DestPort` is the UDP port of the remote peer we're trying to connect to.
* `DestIP` is the IP of the remote peer we're trying to connect to.
* `Error` indicates any errors that the server encountered. On success this will be `""`. If `Error` is set, none 
  of the other fields are guaranteed to be valid.

