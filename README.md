a custom logging library

```go

package main

import (
	"bytes"
	"net"

	"github.com/rexlx/logary"
)

func main() {
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

	udpLogger := &UDPLogger{Addr: ":5140", Log: jsonLogger}
	udpLogger.receiveDataOverUDP()

}
type UDPLogger struct {
	Log          *logary.Logger
	Addr         string
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
	}
}

func (u *UDPLogger) writeToLog(data []byte) {
	u.Log.Debugf("%s", bytes.TrimRight(data, "\n"))
}

```