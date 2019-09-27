package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const goperfVersion string = "0.7.1"

type droppedTicksTracker struct {
	sync.Mutex
	N uint64
}

type LogData struct {
	sync.Mutex
	NBytes          int
	JitterSum       uint64
	JitterN         uint64
	NPktsSent       uint64
	NPktsRecd       uint64
	NPktsDropped    uint64
	NPktsOutOfOrder uint64
	RcvdSeqNumber   uint64
	SentSeqNumber   uint64

	NSec uint64

	NWrites       uint64
	NDroppedTicks uint64

	InUse    bool
	Shutdown bool
}

const N_LINES_HDR_REFRESH = 24

var prevNDroppedTicks uint64 = 0

type historyTrack struct {
	current      uint64
	last10       [10]uint64
	nFilled      int
	nextInLast10 int
	totalEver    uint64
	totalLast10  uint64
}

var dt droppedTicksTracker

var exitCode = 0

func main() {
	defer func() {
		os.Exit(exitCode)
	}()

	cPtr := flag.String("c", "", "c host:port, client, make connection to host:port")
	sPtr := flag.String("s", "", "s N, server, listen on port N (all interfaces)")
	scrollPtr := flag.Bool("scroll", false, "makes output scroll")
	tsPtr := flag.Bool("ts", false, "print timestamp")
	nbPtr := flag.Int64("nb", -1, "nb nnn, send/receive nnn bytes then quit (default: no byte limit)")
	nsPtr := flag.Int64("ns", -1, "ns nnn, send/receive for nnn seconds, then quit (default: no time limit)")
	vPtr := flag.Bool("v", false, "v, display version and quit")
	ratePtr := flag.String("rate", "", "rate nnn[X], specify rate in bps, with an optional multiplier X (K, M, or G)")

	flag.Parse()
	var rate int = 0
	var success bool

	if *vPtr != false {
		fmt.Printf("iperf: Version %v \n", goperfVersion)
		return
	}

	if *ratePtr != "" {
		rate, success = convertToBps(*ratePtr)
		if !success {
			fmt.Println("Invalid rate argument", *ratePtr, rate)
			exitCode = 5
			return
		}
		fmt.Printf("rate=%v \n", rate)
	}

	if *cPtr != "" && *sPtr != "" {
		fmt.Println("Error: cannot select both client and server")
		exitCode = 5
		return
	}

	if *cPtr == "" && *sPtr == "" {
		fmt.Println("Error: must select one of either client or server")
		exitCode = 5
		return
	}

	if *sPtr != "" {
		port, err := strconv.Atoi(*sPtr)
		if err != nil {
			fmt.Println("Error: -s value must be numeric characters only")
			exitCode = 5
			return
		}

		if port < 1024 || port > 65535 {
			fmt.Println("Error: -s value's valid range is 1024-65535")
			exitCode = 5
			return
		}
	}

	if *cPtr != "" {
		_, _, err := net.SplitHostPort(*cPtr)
		if err != nil {
			fmt.Println("Error: -c value not a valid host:port")
			exitCode = 5
			return
		}
	}

	if *nbPtr != -1 && *nsPtr != -1 {
		fmt.Printf("-nb (byte limit) and -ns (time limit) both specified, using time limit of %d seconds \n", *nsPtr)
		*nbPtr = -1
	} else if *nbPtr != -1 {
		var direction string
		if *cPtr != "" {
			direction = "send"
		} else {
			direction = "receive"
		}
		fmt.Printf("Will %s %d bytes, then quit \n", direction, *nbPtr)
	} else if *nsPtr != -1 {
		var direction string
		if *cPtr != "" {
			direction = "send"
		} else {
			direction = "receive"
		}
		fmt.Printf("Will %s for %d seconds, then quit \n", direction, *nsPtr)
	}

	if *sPtr != "" {
		exitCode = runTcpServer(*sPtr, *scrollPtr, *tsPtr, *nsPtr, *nbPtr)
		return
	} else {
		exitCode = runTcpClient(*cPtr, *scrollPtr, *tsPtr, *nsPtr, *nbPtr, rate)
		return
	}
}

func runTcpServer(s string, scroll bool, ts bool, ns int64, nb int64) int {

	var connClosed = false
	var dataError = false
	var ctlCRecd = false

	laddr, err := net.ResolveTCPAddr("tcp", "0.0.0.0:"+s)
	if err != nil {
		fmt.Println("Error resolving:", err.Error())
		return 6
	}

	ln, err := net.ListenTCP("tcp", laddr)
	if err != nil {
		fmt.Println("Error listening:", err.Error())
		return 7
	}

	defer ln.Close()

	for {
		fmt.Println("Listening on", "0.0.0.0:"+s)
		conn, err := ln.AcceptTCP()
		if err != nil {
			fmt.Println("Error accepting:", err.Error())
			return 8
		}

		sigc := make(chan os.Signal, 1)
		signal.Notify(sigc, os.Interrupt)

		go func() {
			<-sigc
			fmt.Println()
			ctlCRecd = true
		}()

		var totalBytesRcvd int64 = 0
		var nsec uint64 = 0

		var c LogData
		c.InUse = true

		go LogRates(&c, scroll, ts, true)

		buf := make([]byte, 1024)

		runtime.LockOSThread()

		var i = 0

		for {
			var err error
			var n int

			err = conn.SetReadDeadline(time.Now().Add(time.Millisecond * 500))
			n, err = conn.Read(buf)
			i++
			if err == io.EOF {
				fmt.Println()
				fmt.Println("EOF detected")
				conn.Close()

				connClosed = true
			} else if err != nil {
				s := err.Error()
				fmt.Printf("err type=%T, err=%v, n=%v \n", err, err, n)
				fmt.Printf("s=%v \n\n", s)
				conn.Close()
				connClosed = true
			} else if err = verifyTCPRead(buf, n, totalBytesRcvd); err != nil {
				fmt.Printf("%v", err)
				conn.Close()

				dataError = true
			}

			c.Lock()
			c.NBytes += n
			nsec = c.NSec
			c.Unlock()

			totalBytesRcvd += int64(n)
			if nb != -1 && totalBytesRcvd > nb {
				fmt.Printf("\nByte limit (%d) reached, quitting, %d total bytes received \n", nb, totalBytesRcvd)
				return 20
			}

			if ns != -1 && nsec >= uint64(ns) {
				fmt.Printf("\nTime limit (%d seconds) reached, quitting \n", ns)
				return 21
			}

			if connClosed || dataError || ctlCRecd {
				c.Lock()
				c.Shutdown = true
				c.Unlock()
				for {
					time.Sleep(100 * time.Millisecond)
					c.Lock()
					if c.Shutdown == false {
						c.InUse = false
						c.Unlock()
						break
					}
					c.Unlock()
				}

				signal.Reset(os.Interrupt)

				if ctlCRecd {
					return 32
				}

				totalBytesRcvd = 0
				connClosed = false
				dataError = false
				break
			}
		}
	}
	return 0
}

func verifyTCPRead(buf []byte, n int, totalBytesRcvd int64) error {

	val := byte(totalBytesRcvd % 256)

	for i := 0; i < n; i++ {
		if buf[i] != val {
			return fmt.Errorf("\nERROR: invalid value detected at byte %v: should have been %d, received %v \n       Closing connection \n",
				totalBytesRcvd+int64(i)+1,
				val,
				buf[i])
		}

		val++
	}

	return nil
}

func runTcpClient(hostport string, scroll bool, ts bool, ns int64, nb int64, rate int) int {

	var ctlCRecd bool = false

	fmt.Println("Attempting to make a TCP connection to", hostport)
	raddr, err := net.ResolveTCPAddr("tcp", hostport)
	if err != nil {
		fmt.Println(err.Error())
		return 6
	}

	conn, err := net.DialTCP("tcp", nil, raddr)
	if err != nil {
		fmt.Println(err.Error())
		return 9
	}

	var totalBytesSent int64 = 0
	var nsec uint64 = 0

	// start logger
	var c LogData
	c.InUse = true
	go LogRates(&c, scroll, ts, true)

	// make a channel for keyboard input routing
	k := make(chan rune)
	go kbInput(k)

	// channel for signal notification
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt)

	go func() {
		<-sigc
		ctlCRecd = true
	}()

	if rate != 0 {
		baseWriteSize := rate / 8000
		extra := rate - baseWriteSize*8000
		writeControl := make([]bool, 8000)

		for i := 0; i < 8000; i++ {
			writeControl[i] = false
		}

		var i int
		for i = 0; i < extra; i++ {
			writeControl[i] = true
		}

		b := make([]byte, 0)
		buf := bytes.NewBuffer(b)

		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()

		var writeControlIdx = 0

		runtime.LockOSThread()

		var retCode int
		var terminating = false

		for {
			select {

			case <-ticker.C:

				var writeSize int

				if writeControl[writeControlIdx] {
					writeSize = baseWriteSize + 1
				} else {
					writeSize = baseWriteSize
				}

				for {
					if writeSize < buf.Len() {
						break
					}
					buf = appendBuffer(buf, 128*1024)
				}

				var n = 0
				var err error

				if writeSize > 0 {
					bb := buf.Next(writeSize)
					conn.SetWriteDeadline(time.Now().Add(time.Millisecond * 500))
					n, err = conn.Write(bb)

					if err != nil {
						fmt.Printf("\nError writing: %v \n", err.Error())
						conn.Close()
						retCode = 10
						terminating = true
					}
				}

				writeControlIdx++
				if writeControlIdx == 8000 {
					writeControlIdx = 0
				}

				c.Lock()

				c.NBytes += n
				nsec = c.NSec
				c.NWrites++

				dt.Lock()
				c.NDroppedTicks = dt.N
				dt.Unlock()

				c.Unlock()

				totalBytesSent += int64(n)
				if nb != -1 && totalBytesSent > nb {
					fmt.Printf("\nSend byte limit (%d) reached, quitting, sent %d bytes \n", nb, totalBytesSent)
					retCode = 20
					terminating = true
				}

				if ns != -1 && nsec >= uint64(ns) {
					fmt.Printf("\nTime limit (%d seconds) reached, quitting \n", ns)
					retCode = 21
					terminating = true
				}
			}

			if ctlCRecd {
				fmt.Printf("\nOperator interrupt, terminating \n")
				retCode = 32
				terminating = true
			}

			if terminating {
				signal.Reset(os.Interrupt)
				return retCode
			}
		}

	} else {
		// create slice to hold values to send
		buf := make([]byte, 128*1024)
		for i := 0; i < 128*1024; i++ {
			buf[i] = (byte)(i % 256)
		}

		runtime.LockOSThread()

		var retCode int
		var terminating bool = false

		for {

			var n int = 0
			var err error

			conn.SetWriteDeadline(time.Now().Add(time.Millisecond * 500))
			n, err = conn.Write(buf)
			if err != nil {
				fmt.Printf("\nError writing: %v \n", err.Error())
				conn.Close()
				retCode = 10
				terminating = true
			}

			c.Lock()
			c.NBytes += n
			nsec = c.NSec
			c.NWrites++
			c.Unlock()

			totalBytesSent += int64(n)

			if nb != -1 && totalBytesSent > nb {
				fmt.Printf("\nSend byte limit (%d) reached, quitting, sent %d bytes \n", nb, totalBytesSent)
				retCode = 20
				terminating = true
			}

			if ns != -1 && nsec >= uint64(ns) {
				fmt.Printf("\nTime limit (%d seconds) reached, quitting \n", ns)
				retCode = 21
				terminating = true
			}

			if ctlCRecd {
				fmt.Printf("\nOperator interrupt, terminating \n")
				retCode = 32
				terminating = true
			}

			if terminating {
				signal.Reset(os.Interrupt)
				return retCode
			}
		}
	}
}

func appendBuffer(b *bytes.Buffer, n int) *bytes.Buffer {

	s := b.Next(b.Len())

	s2 := make([]byte, n)
	for i := 0; i < n; i++ {
		s2[i] = byte(i % 256)
	}
	s = append(s, s2...)
	b2 := bytes.NewBuffer(s)

	return b2
}

func kbInput(ky chan rune) {
	reader := bufio.NewReader(os.Stdin)
	for {
		char, _, err := reader.ReadRune()

		if err != nil {
			fmt.Println(err)
		} else {
			ky <- char
		}
	}
}

func convertToBps(inputRate string) (rate int, success bool) {
	var multiplier int64 = 1

	if strings.HasSuffix(inputRate, "K") {
		multiplier = 1000
		inputRate = strings.TrimSuffix(inputRate, "K")
	}

	if strings.HasSuffix(inputRate, "M") {
		multiplier = 1000000
		inputRate = strings.TrimSuffix(inputRate, "M")
	}

	if strings.HasSuffix(inputRate, "G") {
		multiplier = 1000000000
		inputRate = strings.TrimSuffix(inputRate, "G")
	}

	var s int64
	var err error
	if s, err = strconv.ParseInt(inputRate, 10, 32); err != nil {
		return 0, false
	}

	return int(s * multiplier), true
}

func LogRates(c *LogData, scroll bool, use_ts bool, use_stdout bool) {

	var localLogData LogData
	needHeader := true

	var nsec uint64

	var r historyTrack // rate
	var j historyTrack // jitter

	var line_term string
	var linesToHdrRefresh int
	if scroll == true {
		line_term = "\n"
		linesToHdrRefresh = N_LINES_HDR_REFRESH
	} else {
		line_term = "\r"
	}

	var output io.Writer
	if use_stdout {
		output = os.Stdout
	} else {
		output = os.Stderr
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {

		case <-ticker.C:

			c.Lock()

			if !c.InUse {
				c.Unlock()
				continue
			}

			localLogData = *c

			nsec++

			c.NBytes = 0
			c.JitterSum = 0
			c.JitterN = 0
			c.NWrites = 0
			c.NSec = nsec

			c.Unlock()

			t2 := time.Now()
			t2 = t2.UTC()
			ts := t2.Format("[01-02-2006][15:04:05.000000]")

			updateHistoryTrack(&r, (uint64)(localLogData.NBytes*8))

			var jitterThisPeriod uint64
			if localLogData.JitterN == 0 {
				jitterThisPeriod = 0
			} else {
				jitterThisPeriod = localLogData.JitterSum / localLogData.JitterN
			}
			updateHistoryTrack(&j, jitterThisPeriod)

			if localLogData.Shutdown {
				fmt.Println("Final statistics:")
				displayHeader(output, use_ts)
				writeDataLine(output, use_ts, ts, nsec, r, j, localLogData, line_term, scroll, &linesToHdrRefresh)
				fmt.Println()
				c.Lock()
				c.NSec = 0
				c.Shutdown = false
				c.Unlock()
				return
			} else {
				if needHeader {
					displayHeader(output, use_ts)
					needHeader = false
				}

				writeDataLine(output, use_ts, ts, nsec, r, j, localLogData, line_term, scroll, &linesToHdrRefresh)
			}
		}
	}
}

func writeDataLine(output io.Writer,
	use_ts bool,
	ts string,
	nsec uint64,
	r historyTrack,
	j historyTrack,
	localLogData LogData,
	line_term string,
	scroll bool,
	linesToHdrRefresh *int) {

	if use_ts {
		fmt.Fprintf(output, "%s", ts)
	}

	/* # seconds */
	fmt.Fprintf(output, "[ %7d ]", nsec)

	/* last second */
	fmt.Fprintf(output, "[ %9s ]", formatRate(r.current))

	/* last 10 seconds */
	fmt.Fprintf(output, "[ %9s ]", formatRate(r.totalLast10/(uint64)(r.nFilled)))

	/* total */
	fmt.Fprintf(output, "[ %9s ]", formatRate(r.totalEver/nsec))

	prevNDroppedTicks = localLogData.NDroppedTicks

	fmt.Fprintf(output, line_term)
	if scroll {
		*linesToHdrRefresh--
		if *linesToHdrRefresh == 0 {
			displayHeader(output, use_ts)
			*linesToHdrRefresh = N_LINES_HDR_REFRESH
		}
	}
}

func formatRate(bps uint64) string {

	var label string
	var r float64
	var ret string
	var bpsf float64 = (float64)(bps)

	switch {
	case bps > 1000000000:
		label = "G"
		r = bpsf / 1000000000.

	case bps > 1000000:
		label = "M"
		r = bpsf / 1000000.

	case bps > 1000:
		label = "K"
		r = bpsf / 1000.

	default:
		label = " "
		r = bpsf
	}

	ret = fmt.Sprintf("%5.3f%s", r, label)
	return ret
}

func updateHistoryTrack(r *historyTrack, current uint64) {

	r.current = current

	r.totalEver += r.current
	r.last10[r.nextInLast10] = r.current

	r.nextInLast10++
	if r.nextInLast10 == 10 {
		r.nextInLast10 = 0
	}

	if r.nFilled < 10 {
		r.nFilled++
	}

	/* last 10 seconds */
	r.totalLast10 = 0
	for i := 0; i < r.nFilled; i++ {
		r.totalLast10 += r.last10[i]
	}
}

func displayHeader(output io.Writer, use_ts bool) {
	if use_ts {
		fmt.Fprintf(output, "                             ")
	}
	fmt.Fprintf(output, "           [ <-------- Data Rate (bps) --------> ]\n")


	if use_ts {
		fmt.Fprintf(output, "[                  Timestamp]")
	}
	fmt.Fprintf(output, "[  # Secs ][ Lst Secnd ][  Lst 10 S ][ Snce Strt ]\n")

}
