my log library


```go
package main

import (
	"bytes"
	"net"
	"sync"

	"github.com/rexlx/logary"
)

func main() {
	// create the logary logger
	jsonLogger, err := logary.NewLogger(logary.Config{
		Filename:   "structured.json",
		Structured: true,
		Level:      logary.DebugLevel,
		MaxSizeMB:  10,
		MaxBackups: 3,
	})
	if err != nil {
		panic(err)
	}

	// our example needs a few other things
	mu := &sync.RWMutex{}
	mcache := make([]string, 100)
	// pass our custom logger to the udp logger
	udpLogger := &UDPLogger{Addr: ":5140", Log: jsonLogger, Mutex: mu, MessageCache: mcache}
	// start listening
	udpLogger.receiveDataOverUDP()

}

type UDPLogger struct {
	Log          *logary.Logger
	Mutex        *sync.RWMutex
	Addr         string
	MessageCache []string
}

func (u *UDPLogger) receiveDataOverUDP() {
	serverAddr, err := net.ResolveUDPAddr("udp", u.Addr)
	if err != nil {
		panic(err)
	}
	server, err := net.ListenUDP("udp", serverAddr)
	if err != nil {
		panic(err)
	}
	defer server.Close()
	buf := make([]byte, 1024)
	for {
		n, _, err := server.ReadFromUDP(buf)
		if err != nil {
			panic(err)
		}


		u.writeToLog(buf[:n])
		// go u.writeToLog(buf[:n]). // if youre feeling crazy
	}
}

func (u *UDPLogger) writeToLog(data []byte) {
	u.Log.Debugf("%s", bytes.TrimRight(data, "\n"))
}
```