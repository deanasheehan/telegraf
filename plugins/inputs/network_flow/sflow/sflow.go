// Package sflow contains a Telegraf input plugin that listens for SFLow V5 network flow sample monitoring packets, parses them to extract flow
// samples which it turns into Metrics for output
package sflow

import (
	"fmt"
	"io"
	"log"
	"net"
	"strings"

	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/internal"
	"github.com/influxdata/telegraf/plugins/inputs"
	"github.com/influxdata/telegraf/plugins/parsers"
	"github.com/influxdata/telegraf/plugins/parsers/network_flow/sflow"
)

type setReadBufferer interface {
	SetReadBuffer(bytes int) error
}

type packetListener struct {
	net.PacketConn
	*Listener
}

func (psl *packetListener) listen() {
	buf := make([]byte, 64*1024) // 64kb - maximum size of IP packet
	for {
		n, _, err := psl.ReadFrom(buf)
		if err != nil {
			if !strings.HasSuffix(err.Error(), ": use of closed network connection") {
				psl.AddError(err)
			}
			break
		}
		psl.process(buf[:n])
	}
}

func (psl *packetListener) process(buf []byte) {
	metrics, err := psl.Parse(buf)
	if err != nil {
		psl.AddError(fmt.Errorf("unable to parse incoming packet: %s", err))

	}
	for _, m := range metrics {
		psl.AddMetric(m)
	}
}

// Listener configuration structure
type Listener struct {
	ServiceAddress string        `toml:"service_address"`
	ReadBufferSize internal.Size `toml:"read_buffer_size"`

	MaxFlowsPerSample      uint32 `toml:"max_flows_per_sample"`
	MaxCountersPerSample   uint32 `toml:"max_counters_per_sample"`
	MaxSamplesPerPacket    uint32 `toml:"max_samples_per_packet"`
	MaxSampleLength        uint32 `toml:"max_sample_length"`
	MaxFlowHeaderLength    uint32 `toml:"max_flow_header_length"`
	MaxCounterHeaderLength uint32 `toml:"max_counter_header_length"`

	parsers.Parser
	telegraf.Accumulator
	io.Closer
}

// Description answers a description of this input plugin
func (sl *Listener) Description() string {
	return "SFlow V5 Protocol Listener"
}

// SampleConfig answers a sample configuration
func (sl *Listener) SampleConfig() string {
	return `
	## URL to listen on
	# service_address = "udp://:6343"
	# service_address = "udp4://:6343"
	# service_address = "udp6://:6343"
    
	## Maximum socket buffer size (in bytes when no unit specified).
	## For stream sockets, once the buffer fills up, the sender will start backing up.
	## For datagram sockets, once the buffer fills up, metrics will start dropping.
	## Defaults to the OS default.
	# read_buffer_size = "64KiB"
	`
}

// Gather is a NOP for sFlow as it receives, asynchronously, sFlow network packets
func (sl *Listener) Gather(_ telegraf.Accumulator) error {
	return nil
}

func (sl *Listener) getSflowConfig() sflow.V5FormatOptions {
	sflowConfig := sflow.NewDefaultV5FormatOptions()
	if sl.MaxFlowsPerSample > 0 {
		sflowConfig.MaxFlowsPerSample = sl.MaxFlowsPerSample
	}
	if sl.MaxCountersPerSample > 0 {
		sflowConfig.MaxCountersPerSample = sl.MaxCountersPerSample
	}
	if sl.MaxSamplesPerPacket > 0 {
		sflowConfig.MaxSamplesPerPacket = sl.MaxSamplesPerPacket
	}
	if sl.MaxSampleLength > 0 {
		sflowConfig.MaxSampleLength = sl.MaxSampleLength
	}
	if sl.MaxFlowHeaderLength > 0 {
		sflowConfig.MaxFlowHeaderLength = sl.MaxFlowHeaderLength
	}
	if sl.MaxCounterHeaderLength > 0 {
		sflowConfig.MaxCounterHeaderLength = sl.MaxCounterHeaderLength
	}
	return sflowConfig
}

// Start starts this sFlow listener listening on the configured network for sFlow packets
func (sl *Listener) Start(acc telegraf.Accumulator) error {
	sl.Accumulator = acc

	parser, err := sflow.NewParser("sflow", make(map[string]string), sl.getSflowConfig())
	if err != nil {
		return err
	}
	sl.Parser = parser

	spl := strings.SplitN(sl.ServiceAddress, "://", 2)
	if len(spl) != 2 {
		return fmt.Errorf("invalid service address: %s", sl.ServiceAddress)
	}

	protocol := spl[0]
	addr := spl[1]

	pc, err := newUDPListener(protocol, addr)
	if err != nil {
		return err
	}
	if sl.ReadBufferSize.Size > 0 {
		if srb, ok := pc.(setReadBufferer); ok {
			srb.SetReadBuffer(int(sl.ReadBufferSize.Size))
		} else {
			log.Printf("W! Unable to set read buffer on a %s socket", protocol)
		}
	}

	log.Printf("I! [inputs.sflow] Listening on %s://%s", protocol, pc.LocalAddr())

	psl := &packetListener{
		PacketConn: pc,
		Listener:   sl,
	}

	sl.Closer = psl
	go psl.listen()

	return nil
}

// Stop this Listener
func (sl *Listener) Stop() {
	if sl.Closer != nil {
		sl.Close()
		sl.Closer = nil
	}
}

// newListener constructs a new vanilla, unconfigured, listener and returns it
func newListener() *Listener {
	p, _ := sflow.NewParser("sflow", make(map[string]string), sflow.NewDefaultV5FormatOptions())
	return &Listener{Parser: p}
}

// newUDPListener answers a net.PacketConn for the expected UDP network and address passed in
func newUDPListener(network string, address string) (net.PacketConn, error) {
	switch network {
	case "udp", "udp4", "udp6":
		addr, err := net.ResolveUDPAddr(network, address)
		if err != nil {
			return nil, err
		}
		return net.ListenUDP(network, addr)
	default:
		return nil, fmt.Errorf("unsupported network type %s", network)
	}
}

// init registers this SFflow input plug in with the Telegraf framework
func init() {
	inputs.Add("sflow", func() telegraf.Input { return newListener() })
}
