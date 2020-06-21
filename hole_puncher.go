package main

import (
	"net"
	"log"
	"sync"
	"time"
	"fmt"
	"encoding/json"
)

var entry_timeout = 90 * time.Second

type Request struct {
	IP string
}

type Response struct {
	SourcePort int
	SourceIP string
	DestPort int
	DestIP string
	Error string
}

type IPTuple struct {
	addr string
	port int
}

type IPTupleMessage struct { //sent over channel to TCP waiter, can represent failure/timeout
	ip IPTuple
	ok bool
}

/*
 * entries containing active UDP connections
 * if the UDP connection gets there first, it populates the IPTuple.
 * if the TCP connection gets there first, it populates the waiting_channel and waits for it.
 */
type IPEntry struct {
	address IPTuple
	ip_valid bool //IPTuple can be uninitialized if the struct is created by a TCP waiter

	waiting_channels []chan IPTupleMessage //channels to waiting TCP threads

	created time.Time
}

type ServerState struct {
	lock sync.Mutex
	ip_map map[string]*IPEntry
}


func (state ServerState) AddIPEntry(addr string, port int) {
	state.lock.Lock()
	defer state.lock.Unlock()

	//check if entry already exists, and notifies waiting TCP thread if it exists
	if entry, ok := state.ip_map[addr]; ok {
		for _, channel := range entry.waiting_channels {
			channel <- IPTupleMessage{IPTuple{addr, port}, true}
		}

		//make sure the values are populated for future requests, and reset the creation time
		entry.address = IPTuple{addr, port}
		entry.ip_valid = true
		entry.created = time.Now()
		return
	}

	//if the TCP channel isn't here yet, just populate the IPTuple values for it when it arrives
	state.ip_map[addr] = &IPEntry{
		address: IPTuple{addr, port},
		ip_valid: true,
		created: time.Now(),
	}
}

//checks if an entry for the given address exists, and returns the corresponding IPTuple
//if it doesn't, it sets up a waiter channel and returns it, along with false for "ok"
func (state ServerState) GetIPEntry(addr string, waiter_channel chan IPTupleMessage)  (IPTuple, bool) {
	state.lock.Lock()
	defer state.lock.Unlock()

		//check if entry exists and use it if it does
	if entry, ok := state.ip_map[addr]; ok {
		if entry.ip_valid {
			return entry.address, true	
		}
		fmt.Println("IP invalid! ", entry)
		entry.waiting_channels = append(entry.waiting_channels, waiter_channel)
		return IPTuple{}, false
	}

	state.ip_map[addr] = &IPEntry{
		waiting_channels: []chan IPTupleMessage{waiter_channel},
		created: time.Now(),
	}
	return IPTuple{}, false
}

func (state ServerState) Prune() { //remove stale entries
	state.lock.Lock()
	defer state.lock.Unlock()

	for key, value := range state.ip_map {
		if time.Since(value.created) > entry_timeout {
			fmt.Printf("Cleaning stale entry for %s\n", key)
			for _, channel := range value.waiting_channels {
				channel <- IPTupleMessage{ok: false} //tell the waiting TCP threads the bad news
			}

			delete(state.ip_map, key) //I'm sorry, little one
		}
	}
}


func listenUDP(state *ServerState, port int) {
	udp_addr := net.UDPAddr{
		Port: port,
		IP: nil, //net.ParseIP("0.0.0.0"),
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
		//add entry for self
		fmt.Printf(
			"Got UDP packet! Adding entry for %s:%d\n",
			addr.IP.String(),
			addr.Port,
		)
		state.AddIPEntry(addr.IP.String(), addr.Port)
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
	resp.SourceIP= conn.RemoteAddr().(*net.TCPAddr).IP.String()
	resp.SourcePort= conn.RemoteAddr().(*net.TCPAddr).Port

	//add pending request to registry and wait if neccessary
	ip_channel := make(chan IPTupleMessage)
	if entry, ok := state.GetIPEntry(req.IP, ip_channel); ok { //we don't need to wait, connection already established
		fmt.Printf("Entry already exists for %s, responding immediately!\n", req.IP)
		resp.DestIP = entry.addr
		resp.DestPort = entry.port
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
	if !message.ok { //entry timed out, big sad
		fmt.Printf("Entry %s timed out! Telling the client the bad news...\n", req.IP)
		resp.Error= "Timeout"

		pkt, err := json.Marshal(resp)
		if err != nil {
			log.Print(err)
			return
		}
		conn.Write(pkt)
		return
	}

	fmt.Printf("Finished waiting for entry %s, sending response...\n", req.IP)
	resp.DestIP = message.ip.addr
	resp.DestPort = message.ip.port
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
		ip_map: make(map[string]*IPEntry),
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