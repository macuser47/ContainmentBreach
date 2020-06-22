package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"sync"
	"time"
)

var entry_timeout = 90 * time.Second

type UDPPacket struct {
	PeerPort int
}

type Request struct {
	IP string
}

type Response struct {
	SourcePort int
	SourceIP   string
	DestPort   int
	DestIP     string
	Error      string
}

type IPTuple struct {
	Addr     string
	Port     int
	PeerPort int
}

type IPTupleMessage struct { //sent over channel to TCP waiter, can represent failure/timeout
	IP IPTuple
	OK bool
}

/*
 * entries containing active UDP connections
 * if the UDP connection gets there first, it populates the IPTuple.
 * if the TCP connection gets there first, it populates waiting_channels and waits for it.
 */
type IPEntry struct {
	Address IPTuple
	IPValid bool //IPTuple can be uninitialized if the struct is created by a TCP waiter

	WaitingChannels []chan IPTupleMessage //channels to waiting TCP threads

	Created time.Time
}

type ServerState struct {
	Lock    sync.RWMutex
	PeerMap map[string]*IPEntry
}

func (state ServerState) AddIPEntry(addr string, port int, peer_port int) {
	state.Lock.Lock()
	defer state.Lock.Unlock()

	//check if entry already exists, and notifies waiting TCP thread if it exists
	if entry, ok := state.PeerMap[addr]; ok {
		for _, channel := range entry.WaitingChannels {
			channel <- IPTupleMessage{IPTuple{addr, port, peer_port}, true}
		}

		//make sure the values are populated for future requests, and reset the creation time
		entry.Address = IPTuple{addr, port, peer_port}
		entry.IPValid = true
		entry.Created = time.Now()
		return
	}

	//if the TCP channel isn't here yet, just populate the IPTuple values for it when it arrives
	state.PeerMap[addr] = &IPEntry{
		Address: IPTuple{addr, port, peer_port},
		IPValid: true,
		Created: time.Now(),
	}
}

//checks if an entry for the given address exists, and returns the corresponding IPTuple
//if it doesn't, it sets up a waiter channel and returns it, along with false for "ok"
func (state ServerState) GetIPEntry(addr string, waiter_channel chan IPTupleMessage) (IPTuple, bool) {
	state.Lock.Lock()
	defer state.Lock.Unlock()

	//check if entry exists and use it if it does
	if entry, ok := state.PeerMap[addr]; ok {
		if entry.IPValid {
			return entry.Address, true
		}
		fmt.Println("IP invalid! ", entry)
		entry.WaitingChannels = append(entry.WaitingChannels, waiter_channel)
		return IPTuple{}, false
	}

	state.PeerMap[addr] = &IPEntry{
		WaitingChannels: []chan IPTupleMessage{waiter_channel},
		Created:         time.Now(),
	}
	return IPTuple{}, false
}

//get entry from ip map, but without failure/channel mechanism
func (state ServerState) GetEntry(addr string) (*IPEntry, bool) {
	state.Lock.RLock()
	defer state.Lock.RUnlock()

	value, ok := state.PeerMap[addr]
	return value, ok
}

func (state ServerState) Prune() { //remove stale entries
	state.Lock.Lock()
	defer state.Lock.Unlock()

	for key, value := range state.PeerMap {
		if time.Since(value.Created) > entry_timeout {
			fmt.Printf("Cleaning stale entry for %s\n", key)
			for _, channel := range value.WaitingChannels {
				channel <- IPTupleMessage{OK: false} //tell the waiting TCP threads the bad news
			}

			delete(state.PeerMap, key) //I'm sorry, little one
		}
	}
}

func listenUDP(state *ServerState, port int) {
	udp_addr := net.UDPAddr{
		Port: port,
		IP:   nil, //net.ParseIP("0.0.0.0"),
	}
	conn, error := net.ListenUDP("udp", &udp_addr)
	if error != nil {
		log.Fatal(error)
	}

	buf := make([]byte, 1024)
	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			log.Print(err)
			continue
		}

		if addr == nil {
			fmt.Printf("Addr nil (%d)??\n", n)
			continue
		}

		//parse contents
		var packet UDPPacket
		json_bytes := buf[:n]
		if err := json.Unmarshal(json_bytes, &packet); err != nil {
			log.Print(err)
			continue
		}

		//add entry for self
		fmt.Printf(
			"Got UDP packet! Adding entry for %s:%d with peer port: %d\n",
			addr.IP.String(),
			addr.Port,
			packet.PeerPort,
		)
		state.AddIPEntry(addr.IP.String(), addr.Port, packet.PeerPort)
	}
}

func handleTCPConnection(conn net.Conn, state *ServerState) {
	defer conn.Close()

	var req Request
	bytes := make([]byte, 1024)
	len, err := conn.Read(bytes)
	if err != nil {
		log.Print(err)
		return
	}
	byte_slice := bytes[:len]

	if err := json.Unmarshal(byte_slice, &req); err != nil {
		log.Print(err)
		return
	}
	fmt.Printf("Got valid TCP request for %s from %s\n",
		req.IP, conn.RemoteAddr().String())

	var resp Response

	//populate source entries from the UDP entry (if it's not there, that's on the client)
	sender_addr := conn.RemoteAddr().(*net.TCPAddr).IP.String()
	fmt.Printf("Looking up '%s'\n", sender_addr)
	ip_ent, ok := state.GetEntry(sender_addr)
	if !ok {
		fmt.Printf("TCP request was for a UDP entry that doesn't exist! Sending error...\n")
		resp.Error = "No UDP received"

		pkt, err := json.Marshal(resp)
		if err != nil {
			log.Print(err)
			return
		}
		conn.Write(pkt)
		return
	}
	resp.SourceIP = ip_ent.Address.Addr
	resp.SourcePort = ip_ent.Address.Port

	//add pending request for destination data to registry and wait if neccessary
	ip_channel := make(chan IPTupleMessage)
	if entry, ok := state.GetIPEntry(req.IP, ip_channel); ok { //we don't need to wait, connection already established
		fmt.Printf("Entry already exists for %s, responding immediately!\n", req.IP)
		resp.DestIP = entry.Addr
		resp.DestPort = entry.Port
		resp.Error = ""

		pkt, err := json.Marshal(resp)
		if err != nil {
			log.Print(err)
			return
		}
		conn.Write(pkt)
		return
	}

	fmt.Printf("No entry for %s, waiting...\n", req.IP)

	//UDP connection hasn't come in yet, wait for it with channel
	message := <-ip_channel
	if !message.OK { //entry timed out, big sad
		fmt.Printf("Entry %s timed out! Telling the client the bad news...\n", req.IP)
		resp.Error = "Timeout"

		pkt, err := json.Marshal(resp)
		if err != nil {
			log.Print(err)
			return
		}
		conn.Write(pkt)
		return
	}

	fmt.Printf("Finished waiting for entry %s, sending response...\n", req.IP)
	resp.DestIP = message.IP.Addr
	resp.DestPort = message.IP.Port
	resp.Error = ""
	pkt, err := json.Marshal(resp)
	if err != nil {
		log.Print(err)
		return
	}
	conn.Write(pkt)
}

func listenTCP(state *ServerState, port int) {
	sock, error := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if error != nil {
		log.Fatal(error)
	}

	for {
		conn, err := sock.Accept()
		if err != nil {
			log.Print(err)
			continue
		}
		go handleTCPConnection(conn, state)
	}
}

func main() {
	var state = ServerState{
		PeerMap: make(map[string]*IPEntry),
	}
	port := 6969
	fmt.Printf("Starting Containment Breach Hole-Punching server on port %d\n", port)

	go listenUDP(&state, port)
	go listenTCP(&state, port)

	//periodically prune the ip_map
	for {
		time.Sleep(100 * time.Millisecond)
		state.Prune()
	}
}
