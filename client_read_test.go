package gortsplib

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/ipv4"

	"github.com/aler9/gortsplib/pkg/auth"
	"github.com/aler9/gortsplib/pkg/base"
	"github.com/aler9/gortsplib/pkg/headers"
	"github.com/aler9/gortsplib/pkg/rtcpsender"
)

func TestClientReadTracks(t *testing.T) {
	track1, err := NewTrackH264(96, []byte{0x01, 0x02, 0x03, 0x04}, []byte{0x01, 0x02, 0x03, 0x04}, nil)
	require.NoError(t, err)

	track2, err := NewTrackAAC(96, 2, 44100, 2, nil)
	require.NoError(t, err)

	track3, err := NewTrackAAC(96, 2, 96000, 2, nil)
	require.NoError(t, err)

	l, err := net.Listen("tcp", "localhost:8554")
	require.NoError(t, err)
	defer l.Close()

	serverDone := make(chan struct{})
	defer func() { <-serverDone }()
	go func() {
		defer close(serverDone)

		conn, err := l.Accept()
		require.NoError(t, err)
		defer conn.Close()
		br := bufio.NewReader(conn)
		var bb bytes.Buffer

		req, err := readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Options, req.Method)

		base.Response{
			StatusCode: base.StatusOK,
			Header: base.Header{
				"Public": base.HeaderValue{strings.Join([]string{
					string(base.Describe),
					string(base.Setup),
					string(base.Play),
				}, ", ")},
			},
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Describe, req.Method)
		require.Equal(t, mustParseURL("rtsp://localhost:8554/teststream"), req.URL)

		tracks := Tracks{track1, track2, track3}
		tracks.setControls()

		base.Response{
			StatusCode: base.StatusOK,
			Header: base.Header{
				"Content-Type": base.HeaderValue{"application/sdp"},
				"Content-Base": base.HeaderValue{"rtsp://localhost:8554/teststream/"},
			},
			Body: tracks.Write(false),
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		for i := 0; i < 3; i++ {
			req, err := readRequest(br)
			require.NoError(t, err)
			require.Equal(t, base.Setup, req.Method)
			require.Equal(t, mustParseURL(fmt.Sprintf("rtsp://localhost:8554/teststream/trackID=%d", i)), req.URL)

			var inTH headers.Transport
			err = inTH.Read(req.Header["Transport"])
			require.NoError(t, err)

			th := headers.Transport{
				Delivery: func() *headers.TransportDelivery {
					v := headers.TransportDeliveryUnicast
					return &v
				}(),
				Protocol:    headers.TransportProtocolUDP,
				ClientPorts: inTH.ClientPorts,
				ServerPorts: &[2]int{34556 + i*2, 34557 + i*2},
			}

			base.Response{
				StatusCode: base.StatusOK,
				Header: base.Header{
					"Transport": th.Write(),
				},
			}.Write(&bb)
			_, err = conn.Write(bb.Bytes())
			require.NoError(t, err)
		}

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Play, req.Method)
		require.Equal(t, mustParseURL("rtsp://localhost:8554/teststream/"), req.URL)

		base.Response{
			StatusCode: base.StatusOK,
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Teardown, req.Method)
		require.Equal(t, mustParseURL("rtsp://localhost:8554/teststream/"), req.URL)

		base.Response{
			StatusCode: base.StatusOK,
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)
	}()

	c := Client{}

	err = c.StartReading("rtsp://localhost:8554/teststream")
	require.NoError(t, err)
	defer c.Close()

	require.Equal(t, Tracks{track1, track2, track3}, c.Tracks())
}

func TestClientRead(t *testing.T) {
	for _, transport := range []string{
		"udp",
		"multicast",
		"tcp",
		"tls",
	} {
		t.Run(transport, func(t *testing.T) {
			packetRecv := make(chan struct{})

			listenIP := multicastCapableIP(t)
			l, err := net.Listen("tcp", listenIP+":8554")
			require.NoError(t, err)
			defer l.Close()

			var scheme string
			if transport == "tls" {
				scheme = "rtsps"

				cert, err := tls.X509KeyPair(serverCert, serverKey)
				require.NoError(t, err)

				l = tls.NewListener(l, &tls.Config{Certificates: []tls.Certificate{cert}})
			} else {
				scheme = "rtsp"
			}

			serverDone := make(chan struct{})
			defer func() { <-serverDone }()
			go func() {
				defer close(serverDone)

				conn, err := l.Accept()
				require.NoError(t, err)
				defer conn.Close()
				br := bufio.NewReader(conn)
				var bb bytes.Buffer

				req, err := readRequest(br)
				require.NoError(t, err)
				require.Equal(t, base.Options, req.Method)
				require.Equal(t, mustParseURL(scheme+"://"+listenIP+":8554/test/stream?param=value"), req.URL)

				base.Response{
					StatusCode: base.StatusOK,
					Header: base.Header{
						"Public": base.HeaderValue{strings.Join([]string{
							string(base.Describe),
							string(base.Setup),
							string(base.Play),
						}, ", ")},
					},
				}.Write(&bb)
				_, err = conn.Write(bb.Bytes())
				require.NoError(t, err)

				req, err = readRequest(br)
				require.NoError(t, err)
				require.Equal(t, base.Describe, req.Method)
				require.Equal(t, mustParseURL(scheme+"://"+listenIP+":8554/test/stream?param=value"), req.URL)

				track, err := NewTrackH264(96, []byte{0x01, 0x02, 0x03, 0x04}, []byte{0x01, 0x02, 0x03, 0x04}, nil)
				require.NoError(t, err)

				tracks := Tracks{track}
				tracks.setControls()

				base.Response{
					StatusCode: base.StatusOK,
					Header: base.Header{
						"Content-Type": base.HeaderValue{"application/sdp"},
						"Content-Base": base.HeaderValue{scheme + "://" + listenIP + ":8554/test/stream?param=value/"},
					},
					Body: tracks.Write(false),
				}.Write(&bb)
				_, err = conn.Write(bb.Bytes())
				require.NoError(t, err)

				req, err = readRequest(br)
				require.NoError(t, err)
				require.Equal(t, base.Setup, req.Method)
				require.Equal(t, mustParseURL(scheme+"://"+listenIP+":8554/test/stream?param=value/trackID=0"), req.URL)

				var inTH headers.Transport
				err = inTH.Read(req.Header["Transport"])
				require.NoError(t, err)

				th := headers.Transport{}

				var l1 net.PacketConn
				var l2 net.PacketConn

				switch transport {
				case "udp":
					v := headers.TransportDeliveryUnicast
					th.Delivery = &v
					th.Protocol = headers.TransportProtocolUDP
					th.ClientPorts = inTH.ClientPorts
					th.ServerPorts = &[2]int{34556, 34557}

					l1, err = net.ListenPacket("udp", listenIP+":34556")
					require.NoError(t, err)
					defer l1.Close()

					l2, err = net.ListenPacket("udp", listenIP+":34557")
					require.NoError(t, err)
					defer l2.Close()

				case "multicast":
					v := headers.TransportDeliveryMulticast
					th.Delivery = &v
					th.Protocol = headers.TransportProtocolUDP
					v2 := net.ParseIP("224.1.0.1")
					th.Destination = &v2
					th.Ports = &[2]int{25000, 25001}

					l1, err = net.ListenPacket("udp", "224.0.0.0:25000")
					require.NoError(t, err)
					defer l1.Close()

					p := ipv4.NewPacketConn(l1)

					intfs, err := net.Interfaces()
					require.NoError(t, err)

					for _, intf := range intfs {
						err := p.JoinGroup(&intf, &net.UDPAddr{IP: net.ParseIP("224.1.0.1")})
						require.NoError(t, err)
					}

					l2, err = net.ListenPacket("udp", "224.0.0.0:25001")
					require.NoError(t, err)
					defer l2.Close()

					p = ipv4.NewPacketConn(l2)

					intfs, err = net.Interfaces()
					require.NoError(t, err)

					for _, intf := range intfs {
						err := p.JoinGroup(&intf, &net.UDPAddr{IP: net.ParseIP("224.1.0.1")})
						require.NoError(t, err)
					}

				case "tcp", "tls":
					v := headers.TransportDeliveryUnicast
					th.Delivery = &v
					th.Protocol = headers.TransportProtocolTCP
					th.InterleavedIDs = &[2]int{0, 1}
				}

				base.Response{
					StatusCode: base.StatusOK,
					Header: base.Header{
						"Transport": th.Write(),
					},
				}.Write(&bb)
				_, err = conn.Write(bb.Bytes())
				require.NoError(t, err)

				req, err = readRequest(br)
				require.NoError(t, err)
				require.Equal(t, base.Play, req.Method)
				require.Equal(t, mustParseURL(scheme+"://"+listenIP+":8554/test/stream?param=value/"), req.URL)
				require.Equal(t, base.HeaderValue{"npt=0-"}, req.Header["Range"])

				base.Response{
					StatusCode: base.StatusOK,
				}.Write(&bb)
				_, err = conn.Write(bb.Bytes())
				require.NoError(t, err)

				// server -> client
				switch transport {
				case "udp":
					time.Sleep(1 * time.Second)
					l1.WriteTo([]byte{0x01, 0x02, 0x03, 0x04}, &net.UDPAddr{
						IP:   net.ParseIP("127.0.0.1"),
						Port: th.ClientPorts[0],
					})

				case "multicast":
					time.Sleep(1 * time.Second)
					l1.WriteTo([]byte{0x01, 0x02, 0x03, 0x04}, &net.UDPAddr{
						IP:   net.ParseIP("224.1.0.1"),
						Port: 25000,
					})

				case "tcp", "tls":
					base.InterleavedFrame{
						Channel: 0,
						Payload: []byte{0x01, 0x02, 0x03, 0x04},
					}.Write(&bb)
					_, err = conn.Write(bb.Bytes())
					require.NoError(t, err)
				}

				// client -> server (RTCP)
				switch transport {
				case "udp", "multicast":
					// skip firewall opening
					buf := make([]byte, 2048)
					_, _, err := l2.ReadFrom(buf)
					require.NoError(t, err)

					buf = make([]byte, 2048)
					n, _, err := l2.ReadFrom(buf)
					require.NoError(t, err)
					require.Equal(t, []byte{0x05, 0x06, 0x07, 0x08}, buf[:n])
					close(packetRecv)

				case "tcp", "tls":
					var f base.InterleavedFrame
					f.Payload = make([]byte, 2048)
					err := f.Read(br)
					require.NoError(t, err)
					require.Equal(t, 1, f.Channel)
					require.Equal(t, []byte{0x05, 0x06, 0x07, 0x08}, f.Payload)
					close(packetRecv)
				}

				req, err = readRequest(br)
				require.NoError(t, err)
				require.Equal(t, base.Teardown, req.Method)
				require.Equal(t, mustParseURL(scheme+"://"+listenIP+":8554/test/stream?param=value/"), req.URL)

				base.Response{
					StatusCode: base.StatusOK,
				}.Write(&bb)
				_, err = conn.Write(bb.Bytes())
				require.NoError(t, err)
			}()

			counter := 0

			c := &Client{
				TLSConfig: &tls.Config{
					InsecureSkipVerify: true,
				},
				Transport: func() *Transport {
					switch transport {
					case "udp":
						v := TransportUDP
						return &v

					case "multicast":
						v := TransportUDPMulticast
						return &v

					default: // tcp, tls
						v := TransportTCP
						return &v
					}
				}(),
			}

			c.OnPacketRTP = func(trackID int, payload []byte) {
				// ignore multicast loopback
				if transport == "multicast" {
					counter++
					if counter <= 1 || counter >= 3 {
						return
					}
				}

				require.Equal(t, 0, trackID)
				require.Equal(t, []byte{0x01, 0x02, 0x03, 0x04}, payload)

				err := c.WritePacketRTCP(0, []byte{0x05, 0x06, 0x07, 0x08})
				require.NoError(t, err)
			}

			err = c.StartReading(scheme + "://" + listenIP + ":8554/test/stream?param=value")
			require.NoError(t, err)
			defer c.Close()

			<-packetRecv
		})
	}
}

func TestClientReadNonStandardFrameSize(t *testing.T) {
	refPayload := bytes.Repeat([]byte{0x01, 0x02, 0x03, 0x04, 0x05}, 4096/5)

	l, err := net.Listen("tcp", "localhost:8554")
	require.NoError(t, err)
	defer l.Close()

	serverDone := make(chan struct{})
	defer func() { <-serverDone }()
	go func() {
		defer close(serverDone)

		conn, err := l.Accept()
		require.NoError(t, err)
		defer conn.Close()
		br := bufio.NewReader(conn)
		var bb bytes.Buffer

		req, err := readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Options, req.Method)
		require.Equal(t, mustParseURL("rtsp://localhost:8554/teststream"), req.URL)

		base.Response{
			StatusCode: base.StatusOK,
			Header: base.Header{
				"Public": base.HeaderValue{strings.Join([]string{
					string(base.Describe),
					string(base.Setup),
					string(base.Play),
				}, ", ")},
			},
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Describe, req.Method)
		require.Equal(t, mustParseURL("rtsp://localhost:8554/teststream"), req.URL)

		track, err := NewTrackH264(96, []byte{0x01, 0x02, 0x03, 0x04}, []byte{0x01, 0x02, 0x03, 0x04}, nil)
		require.NoError(t, err)

		tracks := Tracks{track}
		tracks.setControls()

		base.Response{
			StatusCode: base.StatusOK,
			Header: base.Header{
				"Content-Type": base.HeaderValue{"application/sdp"},
				"Content-Base": base.HeaderValue{"rtsp://localhost:8554/teststream/"},
			},
			Body: tracks.Write(false),
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Setup, req.Method)
		require.Equal(t, mustParseURL("rtsp://localhost:8554/teststream/trackID=0"), req.URL)

		th := headers.Transport{
			Delivery: func() *headers.TransportDelivery {
				v := headers.TransportDeliveryUnicast
				return &v
			}(),
			Protocol:       headers.TransportProtocolTCP,
			InterleavedIDs: &[2]int{0, 1},
		}

		base.Response{
			StatusCode: base.StatusOK,
			Header: base.Header{
				"Transport": th.Write(),
			},
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Play, req.Method)
		require.Equal(t, mustParseURL("rtsp://localhost:8554/teststream/"), req.URL)
		require.Equal(t, base.HeaderValue{"npt=0-"}, req.Header["Range"])

		base.Response{
			StatusCode: base.StatusOK,
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		base.InterleavedFrame{
			Channel: 0,
			Payload: refPayload,
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)
	}()

	packetRecv := make(chan struct{})

	c := &Client{
		ReadBufferSize: 4500,
		Transport: func() *Transport {
			v := TransportTCP
			return &v
		}(),
		OnPacketRTP: func(trackID int, payload []byte) {
			require.Equal(t, 0, trackID)
			require.Equal(t, refPayload, payload)
			close(packetRecv)
		},
	}

	err = c.StartReading("rtsp://localhost:8554/teststream")
	require.NoError(t, err)
	defer c.Close()

	<-packetRecv
}

func TestClientReadPartial(t *testing.T) {
	listenIP := multicastCapableIP(t)
	l, err := net.Listen("tcp", listenIP+":8554")
	require.NoError(t, err)
	defer l.Close()

	serverDone := make(chan struct{})
	defer func() { <-serverDone }()
	go func() {
		defer close(serverDone)

		conn, err := l.Accept()
		require.NoError(t, err)
		defer conn.Close()
		br := bufio.NewReader(conn)
		var bb bytes.Buffer

		req, err := readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Describe, req.Method)
		require.Equal(t, mustParseURL("rtsp://"+listenIP+":8554/teststream"), req.URL)

		track1, err := NewTrackH264(96, []byte{0x01, 0x02, 0x03, 0x04}, []byte{0x01, 0x02, 0x03, 0x04}, nil)
		require.NoError(t, err)

		track2, err := NewTrackH264(96, []byte{0x01, 0x02, 0x03, 0x04}, []byte{0x01, 0x02, 0x03, 0x04}, nil)
		require.NoError(t, err)

		tracks := Tracks{track1, track2}
		tracks.setControls()

		base.Response{
			StatusCode: base.StatusOK,
			Header: base.Header{
				"Content-Type": base.HeaderValue{"application/sdp"},
				"Content-Base": base.HeaderValue{"rtsp://" + listenIP + ":8554/teststream/"},
			},
			Body: tracks.Write(false),
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Setup, req.Method)
		require.Equal(t, mustParseURL("rtsp://"+listenIP+":8554/teststream/trackID=1"), req.URL)

		var inTH headers.Transport
		err = inTH.Read(req.Header["Transport"])
		require.NoError(t, err)
		require.Equal(t, &[2]int{0, 1}, inTH.InterleavedIDs)

		th := headers.Transport{
			Delivery: func() *headers.TransportDelivery {
				v := headers.TransportDeliveryUnicast
				return &v
			}(),
			Protocol:       headers.TransportProtocolTCP,
			InterleavedIDs: inTH.InterleavedIDs,
		}

		base.Response{
			StatusCode: base.StatusOK,
			Header: base.Header{
				"Transport": th.Write(),
			},
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Play, req.Method)
		require.Equal(t, mustParseURL("rtsp://"+listenIP+":8554/teststream/"), req.URL)

		base.Response{
			StatusCode: base.StatusOK,
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		base.InterleavedFrame{
			Channel: 0,
			Payload: []byte{0x01, 0x02, 0x03, 0x04},
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Teardown, req.Method)
		require.Equal(t, mustParseURL("rtsp://"+listenIP+":8554/teststream/"), req.URL)

		base.Response{
			StatusCode: base.StatusOK,
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)
	}()

	packetRecv := make(chan struct{})

	c := &Client{
		Transport: func() *Transport {
			v := TransportTCP
			return &v
		}(),
		OnPacketRTP: func(trackID int, payload []byte) {
			require.Equal(t, 0, trackID)
			require.Equal(t, []byte{0x01, 0x02, 0x03, 0x04}, payload)
			close(packetRecv)
		},
	}

	u, err := base.ParseURL("rtsp://" + listenIP + ":8554/teststream")
	require.NoError(t, err)

	err = c.Start(u.Scheme, u.Host)
	require.NoError(t, err)
	defer c.Close()

	tracks, baseURL, _, err := c.Describe(u)
	require.NoError(t, err)

	_, err = c.Setup(true, tracks[1], baseURL, 0, 0)
	require.NoError(t, err)

	_, err = c.Play(nil)
	require.NoError(t, err)

	<-packetRecv
}

func TestClientReadNoContentBase(t *testing.T) {
	l, err := net.Listen("tcp", "localhost:8554")
	require.NoError(t, err)
	defer l.Close()

	serverDone := make(chan struct{})
	defer func() { <-serverDone }()
	go func() {
		defer close(serverDone)

		conn, err := l.Accept()
		require.NoError(t, err)
		defer conn.Close()
		br := bufio.NewReader(conn)
		var bb bytes.Buffer

		req, err := readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Options, req.Method)

		base.Response{
			StatusCode: base.StatusOK,
			Header: base.Header{
				"Public": base.HeaderValue{strings.Join([]string{
					string(base.Describe),
					string(base.Setup),
					string(base.Play),
				}, ", ")},
			},
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Describe, req.Method)
		require.Equal(t, mustParseURL("rtsp://localhost:8554/teststream"), req.URL)

		track, err := NewTrackH264(96, []byte{0x01, 0x02, 0x03, 0x04}, []byte{0x01, 0x02, 0x03, 0x04}, nil)
		require.NoError(t, err)

		tracks := Tracks{track}
		tracks.setControls()

		base.Response{
			StatusCode: base.StatusOK,
			Header: base.Header{
				"Content-Type": base.HeaderValue{"application/sdp"},
			},
			Body: tracks.Write(false),
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Setup, req.Method)
		require.Equal(t, mustParseURL("rtsp://localhost:8554/teststream/trackID=0"), req.URL)

		var inTH headers.Transport
		err = inTH.Read(req.Header["Transport"])
		require.NoError(t, err)

		th := headers.Transport{
			Delivery: func() *headers.TransportDelivery {
				v := headers.TransportDeliveryUnicast
				return &v
			}(),
			Protocol:    headers.TransportProtocolUDP,
			ClientPorts: inTH.ClientPorts,
			ServerPorts: &[2]int{34556, 34557},
		}

		base.Response{
			StatusCode: base.StatusOK,
			Header: base.Header{
				"Transport": th.Write(),
			},
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Play, req.Method)
		require.Equal(t, mustParseURL("rtsp://localhost:8554/teststream"), req.URL)

		base.Response{
			StatusCode: base.StatusOK,
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Teardown, req.Method)
		require.Equal(t, mustParseURL("rtsp://localhost:8554/teststream"), req.URL)

		base.Response{
			StatusCode: base.StatusOK,
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)
	}()

	c := Client{}

	err = c.StartReading("rtsp://localhost:8554/teststream")
	require.NoError(t, err)
	c.Close()
}

func TestClientReadAnyPort(t *testing.T) {
	for _, ca := range []string{
		"zero",
		"zero_one",
		"no",
		"random",
	} {
		t.Run(ca, func(t *testing.T) {
			l, err := net.Listen("tcp", "localhost:8554")
			require.NoError(t, err)
			defer l.Close()

			serverRecv := make(chan struct{})

			serverDone := make(chan struct{})
			defer func() { <-serverDone }()
			go func() {
				defer close(serverDone)

				conn, err := l.Accept()
				require.NoError(t, err)
				defer conn.Close()
				br := bufio.NewReader(conn)
				var bb bytes.Buffer

				req, err := readRequest(br)
				require.NoError(t, err)
				require.Equal(t, base.Options, req.Method)

				base.Response{
					StatusCode: base.StatusOK,
					Header: base.Header{
						"Public": base.HeaderValue{strings.Join([]string{
							string(base.Describe),
							string(base.Setup),
							string(base.Play),
						}, ", ")},
					},
				}.Write(&bb)
				_, err = conn.Write(bb.Bytes())
				require.NoError(t, err)

				req, err = readRequest(br)
				require.NoError(t, err)
				require.Equal(t, base.Describe, req.Method)

				track, err := NewTrackH264(96, []byte{0x01, 0x02, 0x03, 0x04}, []byte{0x01, 0x02, 0x03, 0x04}, nil)
				require.NoError(t, err)

				tracks := Tracks{track}
				tracks.setControls()

				base.Response{
					StatusCode: base.StatusOK,
					Header: base.Header{
						"Content-Type": base.HeaderValue{"application/sdp"},
						"Content-Base": base.HeaderValue{"rtsp://localhost:8554/teststream/"},
					},
					Body: tracks.Write(false),
				}.Write(&bb)
				_, err = conn.Write(bb.Bytes())
				require.NoError(t, err)

				req, err = readRequest(br)
				require.NoError(t, err)
				require.Equal(t, base.Setup, req.Method)

				var th headers.Transport
				err = th.Read(req.Header["Transport"])
				require.NoError(t, err)

				l1a, err := net.ListenPacket("udp", "localhost:13344")
				require.NoError(t, err)
				defer l1a.Close()

				l1b, err := net.ListenPacket("udp", "localhost:23041")
				require.NoError(t, err)
				defer l1b.Close()

				base.Response{
					StatusCode: base.StatusOK,
					Header: base.Header{
						"Transport": headers.Transport{
							Protocol: headers.TransportProtocolUDP,
							Delivery: func() *headers.TransportDelivery {
								v := headers.TransportDeliveryUnicast
								return &v
							}(),
							ClientPorts: th.ClientPorts,
							ServerPorts: func() *[2]int {
								switch ca {
								case "zero":
									return &[2]int{0, 0}

								case "zero_one":
									return &[2]int{0, 1}

								case "no":
									return nil

								default: // random
									return &[2]int{23040, 23041}
								}
							}(),
						}.Write(),
					},
				}.Write(&bb)
				_, err = conn.Write(bb.Bytes())
				require.NoError(t, err)

				req, err = readRequest(br)
				require.NoError(t, err)
				require.Equal(t, base.Play, req.Method)

				base.Response{
					StatusCode: base.StatusOK,
				}.Write(&bb)
				_, err = conn.Write(bb.Bytes())
				require.NoError(t, err)

				time.Sleep(500 * time.Millisecond)

				l1a.WriteTo([]byte{0x01, 0x02, 0x03, 0x04}, &net.UDPAddr{
					IP:   net.ParseIP("127.0.0.1"),
					Port: th.ClientPorts[0],
				})

				if ca == "random" {
					// skip firewall opening
					buf := make([]byte, 2048)
					_, _, err := l1b.ReadFrom(buf)
					require.NoError(t, err)

					buf = make([]byte, 2048)
					n, _, err := l1b.ReadFrom(buf)
					require.NoError(t, err)
					require.Equal(t, buf[:n], []byte{0x05, 0x06, 0x07, 0x08})
					close(serverRecv)
				}
			}()

			packetRecv := make(chan struct{})

			c := &Client{
				AnyPortEnable: true,
				OnPacketRTP: func(trackID int, payload []byte) {
					require.Equal(t, payload, []byte{0x01, 0x02, 0x03, 0x04})
					close(packetRecv)
				},
			}

			err = c.StartReading("rtsp://localhost:8554/teststream")
			require.NoError(t, err)
			defer c.Close()

			<-packetRecv

			if ca == "random" {
				c.WritePacketRTCP(0, []byte{0x05, 0x06, 0x07, 0x08})
				<-serverRecv
			}
		})
	}
}

func TestClientReadAutomaticProtocol(t *testing.T) {
	t.Run("switch after status code", func(t *testing.T) {
		l, err := net.Listen("tcp", "localhost:8554")
		require.NoError(t, err)
		defer l.Close()

		serverDone := make(chan struct{})
		defer func() { <-serverDone }()
		go func() {
			defer close(serverDone)

			conn, err := l.Accept()
			require.NoError(t, err)
			defer conn.Close()
			br := bufio.NewReader(conn)
			var bb bytes.Buffer

			req, err := readRequest(br)
			require.NoError(t, err)
			require.Equal(t, base.Options, req.Method)

			base.Response{
				StatusCode: base.StatusOK,
				Header: base.Header{
					"Public": base.HeaderValue{strings.Join([]string{
						string(base.Describe),
						string(base.Setup),
						string(base.Play),
					}, ", ")},
				},
			}.Write(&bb)
			_, err = conn.Write(bb.Bytes())
			require.NoError(t, err)

			req, err = readRequest(br)
			require.NoError(t, err)
			require.Equal(t, base.Describe, req.Method)

			track, err := NewTrackH264(96, []byte{0x01, 0x02, 0x03, 0x04}, []byte{0x01, 0x02, 0x03, 0x04}, nil)
			require.NoError(t, err)

			tracks := Tracks{track}
			tracks.setControls()

			base.Response{
				StatusCode: base.StatusOK,
				Header: base.Header{
					"Content-Type": base.HeaderValue{"application/sdp"},
					"Content-Base": base.HeaderValue{"rtsp://localhost:8554/teststream/"},
				},
				Body: tracks.Write(false),
			}.Write(&bb)
			_, err = conn.Write(bb.Bytes())
			require.NoError(t, err)

			req, err = readRequest(br)
			require.NoError(t, err)
			require.Equal(t, base.Setup, req.Method)

			base.Response{
				StatusCode: base.StatusUnsupportedTransport,
			}.Write(&bb)
			_, err = conn.Write(bb.Bytes())
			require.NoError(t, err)

			req, err = readRequest(br)
			require.NoError(t, err)
			require.Equal(t, base.Setup, req.Method)

			var inTH headers.Transport
			err = inTH.Read(req.Header["Transport"])
			require.NoError(t, err)
			require.Equal(t, headers.TransportProtocolTCP, inTH.Protocol)

			base.Response{
				StatusCode: base.StatusOK,
				Header: base.Header{
					"Transport": headers.Transport{
						Protocol: headers.TransportProtocolTCP,
						Delivery: func() *headers.TransportDelivery {
							v := headers.TransportDeliveryUnicast
							return &v
						}(),
						InterleavedIDs: &[2]int{0, 1},
					}.Write(),
				},
			}.Write(&bb)
			_, err = conn.Write(bb.Bytes())
			require.NoError(t, err)

			req, err = readRequest(br)
			require.NoError(t, err)
			require.Equal(t, base.Play, req.Method)

			base.Response{
				StatusCode: base.StatusOK,
			}.Write(&bb)
			_, err = conn.Write(bb.Bytes())
			require.NoError(t, err)

			base.InterleavedFrame{
				Channel: 0,
				Payload: []byte("\x00\x00\x00\x00"),
			}.Write(&bb)
			_, err = conn.Write(bb.Bytes())
			require.NoError(t, err)
		}()

		packetRecv := make(chan struct{})

		c := Client{
			OnPacketRTP: func(trackID int, payload []byte) {
				close(packetRecv)
			},
		}

		err = c.StartReading("rtsp://localhost:8554/teststream")
		require.NoError(t, err)
		defer c.Close()

		<-packetRecv
	})

	t.Run("switch after timeout", func(t *testing.T) {
		l, err := net.Listen("tcp", "localhost:8554")
		require.NoError(t, err)
		defer l.Close()

		serverDone := make(chan struct{})
		defer func() { <-serverDone }()
		go func() {
			defer close(serverDone)

			conn, err := l.Accept()
			require.NoError(t, err)
			br := bufio.NewReader(conn)
			var bb bytes.Buffer

			req, err := readRequest(br)
			require.NoError(t, err)
			require.Equal(t, base.Options, req.Method)

			base.Response{
				StatusCode: base.StatusOK,
				Header: base.Header{
					"Public": base.HeaderValue{strings.Join([]string{
						string(base.Describe),
						string(base.Setup),
						string(base.Play),
					}, ", ")},
				},
			}.Write(&bb)
			_, err = conn.Write(bb.Bytes())
			require.NoError(t, err)

			req, err = readRequest(br)
			require.NoError(t, err)
			require.Equal(t, base.Describe, req.Method)

			v := auth.NewValidator("myuser", "mypass", nil)

			base.Response{
				StatusCode: base.StatusUnauthorized,
				Header: base.Header{
					"WWW-Authenticate": v.Header(),
				},
			}.Write(&bb)
			_, err = conn.Write(bb.Bytes())
			require.NoError(t, err)

			req, err = readRequest(br)
			require.NoError(t, err)
			require.Equal(t, base.Describe, req.Method)

			err = v.ValidateRequest(req)
			require.NoError(t, err)

			track, err := NewTrackH264(96, []byte{0x01, 0x02, 0x03, 0x04}, []byte{0x01, 0x02, 0x03, 0x04}, nil)
			require.NoError(t, err)

			tracks := Tracks{track}
			tracks.setControls()

			base.Response{
				StatusCode: base.StatusOK,
				Header: base.Header{
					"Content-Type": base.HeaderValue{"application/sdp"},
					"Content-Base": base.HeaderValue{"rtsp://localhost:8554/teststream/"},
				},
				Body: tracks.Write(false),
			}.Write(&bb)
			_, err = conn.Write(bb.Bytes())
			require.NoError(t, err)

			req, err = readRequest(br)
			require.NoError(t, err)
			require.Equal(t, base.Setup, req.Method)
			require.Equal(t, mustParseURL("rtsp://localhost:8554/teststream/trackID=0"), req.URL)

			var inTH headers.Transport
			err = inTH.Read(req.Header["Transport"])
			require.NoError(t, err)

			th := headers.Transport{
				Delivery: func() *headers.TransportDelivery {
					v := headers.TransportDeliveryUnicast
					return &v
				}(),
				Protocol:    headers.TransportProtocolUDP,
				ServerPorts: &[2]int{34556, 34557},
				ClientPorts: inTH.ClientPorts,
			}

			base.Response{
				StatusCode: base.StatusOK,
				Header: base.Header{
					"Transport": th.Write(),
				},
			}.Write(&bb)
			_, err = conn.Write(bb.Bytes())
			require.NoError(t, err)

			req, err = readRequest(br)
			require.NoError(t, err)
			require.Equal(t, base.Play, req.Method)

			base.Response{
				StatusCode: base.StatusOK,
			}.Write(&bb)
			_, err = conn.Write(bb.Bytes())
			require.NoError(t, err)

			req, err = readRequest(br)
			require.NoError(t, err)
			require.Equal(t, base.Teardown, req.Method)

			base.Response{
				StatusCode: base.StatusOK,
			}.Write(&bb)
			_, err = conn.Write(bb.Bytes())
			require.NoError(t, err)

			conn.Close()

			conn, err = l.Accept()
			require.NoError(t, err)
			br = bufio.NewReader(conn)

			req, err = readRequest(br)
			require.NoError(t, err)
			require.Equal(t, base.Describe, req.Method)

			base.Response{
				StatusCode: base.StatusOK,
				Header: base.Header{
					"Content-Type": base.HeaderValue{"application/sdp"},
					"Content-Base": base.HeaderValue{"rtsp://localhost:8554/teststream/"},
				},
				Body: tracks.Write(false),
			}.Write(&bb)
			_, err = conn.Write(bb.Bytes())
			require.NoError(t, err)

			req, err = readRequest(br)
			require.NoError(t, err)
			require.Equal(t, base.Setup, req.Method)

			v = auth.NewValidator("myuser", "mypass", nil)

			base.Response{
				StatusCode: base.StatusUnauthorized,
				Header: base.Header{
					"WWW-Authenticate": v.Header(),
				},
			}.Write(&bb)
			_, err = conn.Write(bb.Bytes())
			require.NoError(t, err)

			req, err = readRequest(br)
			require.NoError(t, err)
			require.Equal(t, base.Setup, req.Method)
			require.Equal(t, mustParseURL("rtsp://localhost:8554/teststream/trackID=0"), req.URL)

			err = v.ValidateRequest(req)
			require.NoError(t, err)

			inTH = headers.Transport{}
			err = inTH.Read(req.Header["Transport"])
			require.NoError(t, err)

			th = headers.Transport{
				Delivery: func() *headers.TransportDelivery {
					v := headers.TransportDeliveryUnicast
					return &v
				}(),
				Protocol:       headers.TransportProtocolTCP,
				InterleavedIDs: inTH.InterleavedIDs,
			}

			base.Response{
				StatusCode: base.StatusOK,
				Header: base.Header{
					"Transport": th.Write(),
				},
			}.Write(&bb)
			_, err = conn.Write(bb.Bytes())
			require.NoError(t, err)

			req, err = readRequest(br)
			require.NoError(t, err)
			require.Equal(t, base.Play, req.Method)

			base.Response{
				StatusCode: base.StatusOK,
			}.Write(&bb)
			_, err = conn.Write(bb.Bytes())
			require.NoError(t, err)

			base.InterleavedFrame{
				Channel: 0,
				Payload: []byte("\x00\x00\x00\x00"),
			}.Write(&bb)
			_, err = conn.Write(bb.Bytes())
			require.NoError(t, err)

			req, err = readRequest(br)
			require.NoError(t, err)
			require.Equal(t, base.Teardown, req.Method)

			base.Response{
				StatusCode: base.StatusOK,
			}.Write(&bb)
			_, err = conn.Write(bb.Bytes())
			require.NoError(t, err)

			conn.Close()
		}()

		packetRecv := make(chan struct{})

		c := &Client{
			ReadTimeout: 1 * time.Second,
			OnPacketRTP: func(trackID int, payload []byte) {
				close(packetRecv)
			},
		}

		err = c.StartReading("rtsp://myuser:mypass@localhost:8554/teststream")
		require.NoError(t, err)
		defer c.Close()

		<-packetRecv
	})
}

func TestClientReadDifferentInterleavedIDs(t *testing.T) {
	l, err := net.Listen("tcp", "localhost:8554")
	require.NoError(t, err)
	defer l.Close()

	serverDone := make(chan struct{})
	defer func() { <-serverDone }()
	go func() {
		defer close(serverDone)

		conn, err := l.Accept()
		require.NoError(t, err)
		defer conn.Close()
		br := bufio.NewReader(conn)
		var bb bytes.Buffer

		req, err := readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Options, req.Method)

		base.Response{
			StatusCode: base.StatusOK,
			Header: base.Header{
				"Public": base.HeaderValue{strings.Join([]string{
					string(base.Describe),
					string(base.Setup),
					string(base.Play),
				}, ", ")},
			},
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Describe, req.Method)
		require.Equal(t, mustParseURL("rtsp://localhost:8554/teststream"), req.URL)

		track1, err := NewTrackH264(96, []byte{0x01, 0x02, 0x03, 0x04}, []byte{0x01, 0x02, 0x03, 0x04}, nil)
		require.NoError(t, err)

		tracks := Tracks{track1}
		tracks.setControls()

		base.Response{
			StatusCode: base.StatusOK,
			Header: base.Header{
				"Content-Type": base.HeaderValue{"application/sdp"},
				"Content-Base": base.HeaderValue{"rtsp://localhost:8554/teststream/"},
			},
			Body: tracks.Write(false),
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Setup, req.Method)
		require.Equal(t, mustParseURL("rtsp://localhost:8554/teststream/trackID=0"), req.URL)

		var inTH headers.Transport
		err = inTH.Read(req.Header["Transport"])
		require.NoError(t, err)

		th := headers.Transport{
			Delivery: func() *headers.TransportDelivery {
				v := headers.TransportDeliveryUnicast
				return &v
			}(),
			Protocol:       headers.TransportProtocolTCP,
			InterleavedIDs: &[2]int{2, 3},
		}

		base.Response{
			StatusCode: base.StatusOK,
			Header: base.Header{
				"Transport": th.Write(),
			},
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Play, req.Method)
		require.Equal(t, mustParseURL("rtsp://localhost:8554/teststream/"), req.URL)

		base.Response{
			StatusCode: base.StatusOK,
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		base.InterleavedFrame{
			Channel: 2,
			Payload: []byte{0x01, 0x02, 0x03, 0x04},
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Teardown, req.Method)
		require.Equal(t, mustParseURL("rtsp://localhost:8554/teststream/"), req.URL)

		base.Response{
			StatusCode: base.StatusOK,
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)
	}()

	packetRecv := make(chan struct{})

	c := &Client{
		Transport: func() *Transport {
			v := TransportTCP
			return &v
		}(),
		OnPacketRTP: func(trackID int, payload []byte) {
			require.Equal(t, 0, trackID)
			close(packetRecv)
		},
	}

	err = c.StartReading("rtsp://localhost:8554/teststream")
	require.NoError(t, err)
	defer c.Close()

	<-packetRecv
}

func TestClientReadRedirect(t *testing.T) {
	l, err := net.Listen("tcp", "localhost:8554")
	require.NoError(t, err)
	defer l.Close()

	serverDone := make(chan struct{})
	defer func() { <-serverDone }()
	go func() {
		defer close(serverDone)

		conn, err := l.Accept()
		require.NoError(t, err)
		br := bufio.NewReader(conn)
		var bb bytes.Buffer

		req, err := readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Options, req.Method)

		base.Response{
			StatusCode: base.StatusOK,
			Header: base.Header{
				"Public": base.HeaderValue{strings.Join([]string{
					string(base.Describe),
					string(base.Setup),
					string(base.Play),
				}, ", ")},
			},
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Describe, req.Method)

		base.Response{
			StatusCode: base.StatusMovedPermanently,
			Header: base.Header{
				"Location": base.HeaderValue{"rtsp://localhost:8554/test"},
			},
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		conn.Close()

		conn, err = l.Accept()
		require.NoError(t, err)
		defer conn.Close()
		br = bufio.NewReader(conn)

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Options, req.Method)

		base.Response{
			StatusCode: base.StatusOK,
			Header: base.Header{
				"Public": base.HeaderValue{strings.Join([]string{
					string(base.Describe),
					string(base.Setup),
					string(base.Play),
				}, ", ")},
			},
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Describe, req.Method)

		track, err := NewTrackH264(96, []byte{0x01, 0x02, 0x03, 0x04}, []byte{0x01, 0x02, 0x03, 0x04}, nil)
		require.NoError(t, err)

		tracks := Tracks{track}
		tracks.setControls()

		base.Response{
			StatusCode: base.StatusOK,
			Header: base.Header{
				"Content-Type": base.HeaderValue{"application/sdp"},
				"Content-Base": base.HeaderValue{"rtsp://localhost:8554/teststream/"},
			},
			Body: tracks.Write(false),
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Setup, req.Method)

		var th headers.Transport
		err = th.Read(req.Header["Transport"])
		require.NoError(t, err)

		base.Response{
			StatusCode: base.StatusOK,
			Header: base.Header{
				"Transport": headers.Transport{
					Protocol: headers.TransportProtocolUDP,
					Delivery: func() *headers.TransportDelivery {
						v := headers.TransportDeliveryUnicast
						return &v
					}(),
					ClientPorts: th.ClientPorts,
					ServerPorts: &[2]int{34556, 34557},
				}.Write(),
			},
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Play, req.Method)

		base.Response{
			StatusCode: base.StatusOK,
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		time.Sleep(500 * time.Millisecond)

		l1, err := net.ListenPacket("udp", "localhost:34556")
		require.NoError(t, err)
		defer l1.Close()

		l1.WriteTo([]byte("\x00\x00\x00\x00"), &net.UDPAddr{
			IP:   net.ParseIP("127.0.0.1"),
			Port: th.ClientPorts[0],
		})
	}()

	packetRecv := make(chan struct{})

	c := Client{
		OnPacketRTP: func(trackID int, payload []byte) {
			close(packetRecv)
		},
	}

	err = c.StartReading("rtsp://localhost:8554/path1")
	require.NoError(t, err)
	defer c.Close()

	<-packetRecv
}

func TestClientReadPause(t *testing.T) {
	writeFrames := func(inTH *headers.Transport, conn net.Conn, br *bufio.Reader) (chan struct{}, chan struct{}) {
		writerTerminate := make(chan struct{})
		writerDone := make(chan struct{})

		go func() {
			defer close(writerDone)

			var l1 net.PacketConn
			if inTH.Protocol == headers.TransportProtocolUDP {
				var err error
				l1, err = net.ListenPacket("udp", "localhost:34556")
				require.NoError(t, err)
				defer l1.Close()
			}
			var bb bytes.Buffer

			t := time.NewTicker(50 * time.Millisecond)
			defer t.Stop()

			for {
				select {
				case <-t.C:
					if inTH.Protocol == headers.TransportProtocolUDP {
						l1.WriteTo([]byte("\x00\x00\x00\x00"), &net.UDPAddr{
							IP:   net.ParseIP("127.0.0.1"),
							Port: inTH.ClientPorts[0],
						})
					} else {
						base.InterleavedFrame{
							Channel: 0,
							Payload: []byte("\x00\x00\x00\x00"),
						}.Write(&bb)
						conn.Write(bb.Bytes())
					}

				case <-writerTerminate:
					return
				}
			}
		}()

		return writerTerminate, writerDone
	}

	for _, transport := range []string{
		"udp",
		"tcp",
	} {
		t.Run(transport, func(t *testing.T) {
			l, err := net.Listen("tcp", "localhost:8554")
			require.NoError(t, err)
			defer l.Close()

			serverDone := make(chan struct{})
			defer func() { <-serverDone }()
			go func() {
				defer close(serverDone)

				conn, err := l.Accept()
				require.NoError(t, err)
				defer conn.Close()
				br := bufio.NewReader(conn)
				var bb bytes.Buffer

				req, err := readRequest(br)
				require.NoError(t, err)
				require.Equal(t, base.Options, req.Method)

				base.Response{
					StatusCode: base.StatusOK,
					Header: base.Header{
						"Public": base.HeaderValue{strings.Join([]string{
							string(base.Describe),
							string(base.Setup),
							string(base.Play),
						}, ", ")},
					},
				}.Write(&bb)
				_, err = conn.Write(bb.Bytes())
				require.NoError(t, err)

				req, err = readRequest(br)
				require.NoError(t, err)
				require.Equal(t, base.Describe, req.Method)

				track, err := NewTrackH264(96, []byte{0x01, 0x02, 0x03, 0x04}, []byte{0x01, 0x02, 0x03, 0x04}, nil)
				require.NoError(t, err)

				tracks := Tracks{track}
				tracks.setControls()

				base.Response{
					StatusCode: base.StatusOK,
					Header: base.Header{
						"Content-Type": base.HeaderValue{"application/sdp"},
						"Content-Base": base.HeaderValue{"rtsp://localhost:8554/teststream/"},
					},
					Body: tracks.Write(false),
				}.Write(&bb)
				_, err = conn.Write(bb.Bytes())
				require.NoError(t, err)

				req, err = readRequest(br)
				require.NoError(t, err)
				require.Equal(t, base.Setup, req.Method)

				var inTH headers.Transport
				err = inTH.Read(req.Header["Transport"])
				require.NoError(t, err)

				th := headers.Transport{
					Delivery: func() *headers.TransportDelivery {
						v := headers.TransportDeliveryUnicast
						return &v
					}(),
				}

				if transport == "udp" {
					th.Protocol = headers.TransportProtocolUDP
					th.ServerPorts = &[2]int{34556, 34557}
					th.ClientPorts = inTH.ClientPorts
				} else {
					th.Protocol = headers.TransportProtocolTCP
					th.InterleavedIDs = inTH.InterleavedIDs
				}

				base.Response{
					StatusCode: base.StatusOK,
					Header: base.Header{
						"Transport": th.Write(),
					},
				}.Write(&bb)
				_, err = conn.Write(bb.Bytes())
				require.NoError(t, err)

				req, err = readRequest(br)
				require.NoError(t, err)
				require.Equal(t, base.Play, req.Method)

				base.Response{
					StatusCode: base.StatusOK,
				}.Write(&bb)
				_, err = conn.Write(bb.Bytes())
				require.NoError(t, err)

				writerTerminate, writerDone := writeFrames(&inTH, conn, br)

				req, err = readRequest(br)
				require.NoError(t, err)
				require.Equal(t, base.Pause, req.Method)

				close(writerTerminate)
				<-writerDone

				base.Response{
					StatusCode: base.StatusOK,
				}.Write(&bb)
				_, err = conn.Write(bb.Bytes())
				require.NoError(t, err)

				req, err = readRequest(br)
				require.NoError(t, err)
				require.Equal(t, base.Play, req.Method)

				base.Response{
					StatusCode: base.StatusOK,
				}.Write(&bb)
				_, err = conn.Write(bb.Bytes())
				require.NoError(t, err)

				writerTerminate, writerDone = writeFrames(&inTH, conn, br)

				req, err = readRequest(br)
				require.NoError(t, err)
				require.Equal(t, base.Teardown, req.Method)

				close(writerTerminate)
				<-writerDone

				base.Response{
					StatusCode: base.StatusOK,
				}.Write(&bb)
				_, err = conn.Write(bb.Bytes())
				require.NoError(t, err)
			}()

			firstFrame := int32(0)
			packetRecv := make(chan struct{})

			c := &Client{
				Transport: func() *Transport {
					if transport == "udp" {
						v := TransportUDP
						return &v
					}
					v := TransportTCP
					return &v
				}(),
				OnPacketRTP: func(trackID int, payload []byte) {
					if atomic.SwapInt32(&firstFrame, 1) == 0 {
						close(packetRecv)
					}
				},
			}

			err = c.StartReading("rtsp://localhost:8554/teststream")
			require.NoError(t, err)
			defer c.Close()

			<-packetRecv

			_, err = c.Pause()
			require.NoError(t, err)

			firstFrame = int32(0)
			packetRecv = make(chan struct{})

			_, err = c.Play(nil)
			require.NoError(t, err)

			<-packetRecv
		})
	}
}

func TestClientReadRTCPReport(t *testing.T) {
	reportReceived := make(chan struct{})

	l, err := net.Listen("tcp", "localhost:8554")
	require.NoError(t, err)
	defer l.Close()

	serverDone := make(chan struct{})
	defer func() { <-serverDone }()
	go func() {
		defer close(serverDone)

		conn, err := l.Accept()
		require.NoError(t, err)
		defer conn.Close()
		br := bufio.NewReader(conn)
		var bb bytes.Buffer

		req, err := readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Options, req.Method)

		base.Response{
			StatusCode: base.StatusOK,
			Header: base.Header{
				"Public": base.HeaderValue{strings.Join([]string{
					string(base.Describe),
					string(base.Setup),
					string(base.Play),
				}, ", ")},
			},
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Describe, req.Method)

		track, err := NewTrackH264(96, []byte{0x01, 0x02, 0x03, 0x04}, []byte{0x01, 0x02, 0x03, 0x04}, nil)
		require.NoError(t, err)

		tracks := Tracks{track}
		tracks.setControls()

		base.Response{
			StatusCode: base.StatusOK,
			Header: base.Header{
				"Content-Type": base.HeaderValue{"application/sdp"},
				"Content-Base": base.HeaderValue{"rtsp://localhost:8554/teststream/"},
			},
			Body: tracks.Write(false),
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Setup, req.Method)

		var inTH headers.Transport
		err = inTH.Read(req.Header["Transport"])
		require.NoError(t, err)

		l1, err := net.ListenPacket("udp", "localhost:27556")
		require.NoError(t, err)
		defer l1.Close()

		l2, err := net.ListenPacket("udp", "localhost:27557")
		require.NoError(t, err)
		defer l2.Close()

		base.Response{
			StatusCode: base.StatusOK,
			Header: base.Header{
				"Transport": headers.Transport{
					Protocol: headers.TransportProtocolUDP,
					Delivery: func() *headers.TransportDelivery {
						v := headers.TransportDeliveryUnicast
						return &v
					}(),
					ServerPorts: &[2]int{27556, 27557},
					ClientPorts: inTH.ClientPorts,
				}.Write(),
			},
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Play, req.Method)

		base.Response{
			StatusCode: base.StatusOK,
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		// skip firewall opening
		buf := make([]byte, 2048)
		_, _, err = l2.ReadFrom(buf)
		require.NoError(t, err)

		rs := rtcpsender.New(90000)

		byts, _ := (&rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				Marker:         true,
				PayloadType:    96,
				SequenceNumber: 946,
				Timestamp:      54352,
				SSRC:           753621,
			},
			Payload: []byte{0x01, 0x02, 0x03, 0x04},
		}).Marshal()
		_, err = l1.WriteTo(byts, &net.UDPAddr{
			IP:   net.ParseIP("127.0.0.1"),
			Port: inTH.ClientPorts[0],
		})
		require.NoError(t, err)
		rs.ProcessPacketRTP(time.Now(), byts)

		_, err = l2.WriteTo(rs.Report(time.Now()), &net.UDPAddr{
			IP:   net.ParseIP("127.0.0.1"),
			Port: inTH.ClientPorts[1],
		})
		require.NoError(t, err)

		buf = make([]byte, 2048)
		n, _, err := l2.ReadFrom(buf)
		require.NoError(t, err)
		pkt, err := rtcp.Unmarshal(buf[:n])
		require.NoError(t, err)
		rr, ok := pkt[0].(*rtcp.ReceiverReport)
		require.True(t, ok)
		require.Equal(t, &rtcp.ReceiverReport{
			SSRC: rr.SSRC,
			Reports: []rtcp.ReceptionReport{
				{
					SSRC:               rr.Reports[0].SSRC,
					LastSequenceNumber: 946,
					LastSenderReport:   rr.Reports[0].LastSenderReport,
					Delay:              rr.Reports[0].Delay,
				},
			},
			ProfileExtensions: []uint8{},
		}, rr)

		close(reportReceived)

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Teardown, req.Method)

		base.Response{
			StatusCode: base.StatusOK,
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)
	}()

	c := &Client{
		udpReceiverReportPeriod: 1 * time.Second,
	}

	err = c.StartReading("rtsp://localhost:8554/teststream")
	require.NoError(t, err)
	defer c.Close()

	<-reportReceived
}

func TestClientReadErrorTimeout(t *testing.T) {
	for _, transport := range []string{
		"udp",
		"tcp",
		"auto",
	} {
		t.Run(transport, func(t *testing.T) {
			l, err := net.Listen("tcp", "localhost:8554")
			require.NoError(t, err)
			defer l.Close()

			serverDone := make(chan struct{})
			defer func() { <-serverDone }()
			go func() {
				defer close(serverDone)

				conn, err := l.Accept()
				require.NoError(t, err)
				defer conn.Close()
				br := bufio.NewReader(conn)
				var bb bytes.Buffer

				req, err := readRequest(br)
				require.NoError(t, err)
				require.Equal(t, base.Options, req.Method)

				base.Response{
					StatusCode: base.StatusOK,
					Header: base.Header{
						"Public": base.HeaderValue{strings.Join([]string{
							string(base.Describe),
							string(base.Setup),
							string(base.Play),
						}, ", ")},
					},
				}.Write(&bb)
				_, err = conn.Write(bb.Bytes())
				require.NoError(t, err)

				req, err = readRequest(br)
				require.NoError(t, err)
				require.Equal(t, base.Describe, req.Method)

				track, err := NewTrackH264(96, []byte{0x01, 0x02, 0x03, 0x04}, []byte{0x01, 0x02, 0x03, 0x04}, nil)
				require.NoError(t, err)

				tracks := Tracks{track}
				tracks.setControls()

				base.Response{
					StatusCode: base.StatusOK,
					Header: base.Header{
						"Content-Type": base.HeaderValue{"application/sdp"},
						"Content-Base": base.HeaderValue{"rtsp://localhost:8554/teststream/"},
					},
					Body: tracks.Write(false),
				}.Write(&bb)
				_, err = conn.Write(bb.Bytes())
				require.NoError(t, err)

				req, err = readRequest(br)
				require.NoError(t, err)
				require.Equal(t, base.Setup, req.Method)

				var inTH headers.Transport
				err = inTH.Read(req.Header["Transport"])
				require.NoError(t, err)

				th := headers.Transport{
					Delivery: func() *headers.TransportDelivery {
						v := headers.TransportDeliveryUnicast
						return &v
					}(),
				}

				var l1 net.PacketConn
				if transport == "udp" || transport == "auto" {
					var err error
					l1, err = net.ListenPacket("udp", "localhost:34556")
					require.NoError(t, err)
					defer l1.Close()

					th.Protocol = headers.TransportProtocolUDP
					th.ServerPorts = &[2]int{34556, 34557}
					th.ClientPorts = inTH.ClientPorts
				} else {
					th.Protocol = headers.TransportProtocolTCP
					th.InterleavedIDs = inTH.InterleavedIDs
				}

				base.Response{
					StatusCode: base.StatusOK,
					Header: base.Header{
						"Transport": th.Write(),
					},
				}.Write(&bb)
				_, err = conn.Write(bb.Bytes())
				require.NoError(t, err)

				req, err = readRequest(br)
				require.NoError(t, err)
				require.Equal(t, base.Play, req.Method)

				base.Response{
					StatusCode: base.StatusOK,
				}.Write(&bb)
				_, err = conn.Write(bb.Bytes())
				require.NoError(t, err)

				if transport == "udp" || transport == "auto" {
					// write a packet to skip the protocol autodetection feature
					l1.WriteTo([]byte("\x01\x02\x03\x04"), &net.UDPAddr{
						IP:   net.ParseIP("127.0.0.1"),
						Port: th.ClientPorts[0],
					})
				}

				req, err = readRequest(br)
				require.NoError(t, err)
				require.Equal(t, base.Teardown, req.Method)

				base.Response{
					StatusCode: base.StatusOK,
				}.Write(&bb)
				_, err = conn.Write(bb.Bytes())
				require.NoError(t, err)
			}()

			c := &Client{
				Transport: func() *Transport {
					switch transport {
					case "udp":
						v := TransportUDP
						return &v

					case "tcp":
						v := TransportTCP
						return &v
					}
					return nil
				}(),
				InitialUDPReadTimeout: 1 * time.Second,
				ReadTimeout:           1 * time.Second,
			}

			err = c.StartReading("rtsp://localhost:8554/teststream")
			require.NoError(t, err)

			err = c.Wait()

			switch transport {
			case "udp", "auto":
				require.EqualError(t, err, "UDP timeout")

			case "tcp":
				require.EqualError(t, err, "TCP timeout")
			}
		})
	}
}

func TestClientReadIgnoreTCPInvalidTrack(t *testing.T) {
	l, err := net.Listen("tcp", "localhost:8554")
	require.NoError(t, err)
	defer l.Close()

	serverDone := make(chan struct{})
	defer func() { <-serverDone }()
	go func() {
		defer close(serverDone)

		conn, err := l.Accept()
		require.NoError(t, err)
		defer conn.Close()
		br := bufio.NewReader(conn)
		var bb bytes.Buffer

		req, err := readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Options, req.Method)

		base.Response{
			StatusCode: base.StatusOK,
			Header: base.Header{
				"Public": base.HeaderValue{strings.Join([]string{
					string(base.Describe),
					string(base.Setup),
					string(base.Play),
				}, ", ")},
			},
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Describe, req.Method)

		track, err := NewTrackH264(96, []byte{0x01, 0x02, 0x03, 0x04}, []byte{0x01, 0x02, 0x03, 0x04}, nil)
		require.NoError(t, err)

		tracks := Tracks{track}
		tracks.setControls()

		base.Response{
			StatusCode: base.StatusOK,
			Header: base.Header{
				"Content-Type": base.HeaderValue{"application/sdp"},
				"Content-Base": base.HeaderValue{"rtsp://localhost:8554/teststream/"},
			},
			Body: tracks.Write(false),
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Setup, req.Method)

		var inTH headers.Transport
		err = inTH.Read(req.Header["Transport"])
		require.NoError(t, err)

		th := headers.Transport{
			Delivery: func() *headers.TransportDelivery {
				v := headers.TransportDeliveryUnicast
				return &v
			}(),
		}
		th.Protocol = headers.TransportProtocolTCP
		th.InterleavedIDs = inTH.InterleavedIDs

		base.Response{
			StatusCode: base.StatusOK,
			Header: base.Header{
				"Transport": th.Write(),
			},
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Play, req.Method)

		base.Response{
			StatusCode: base.StatusOK,
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		base.InterleavedFrame{
			Channel: 6,
			Payload: []byte{0x01, 0x02, 0x03, 0x04},
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		base.InterleavedFrame{
			Channel: 0,
			Payload: []byte{0x05, 0x06, 0x07, 0x08},
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Teardown, req.Method)

		base.Response{
			StatusCode: base.StatusOK,
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)
	}()

	recv := make(chan struct{})

	c := &Client{
		Transport: func() *Transport {
			v := TransportTCP
			return &v
		}(),
		OnPacketRTP: func(trackID int, payload []byte) {
			close(recv)
		},
	}

	err = c.StartReading("rtsp://localhost:8554/teststream")
	require.NoError(t, err)
	defer c.Close()

	<-recv
}

func TestClientReadSeek(t *testing.T) {
	l, err := net.Listen("tcp", "localhost:8554")
	require.NoError(t, err)
	defer l.Close()

	serverDone := make(chan struct{})
	defer func() { <-serverDone }()
	go func() {
		defer close(serverDone)

		conn, err := l.Accept()
		require.NoError(t, err)
		defer conn.Close()
		br := bufio.NewReader(conn)
		var bb bytes.Buffer

		req, err := readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Options, req.Method)

		base.Response{
			StatusCode: base.StatusOK,
			Header: base.Header{
				"Public": base.HeaderValue{strings.Join([]string{
					string(base.Describe),
					string(base.Setup),
					string(base.Play),
				}, ", ")},
			},
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Describe, req.Method)

		track, err := NewTrackH264(96, []byte{0x01, 0x02, 0x03, 0x04}, []byte{0x01, 0x02, 0x03, 0x04}, nil)
		require.NoError(t, err)

		tracks := Tracks{track}
		tracks.setControls()

		base.Response{
			StatusCode: base.StatusOK,
			Header: base.Header{
				"Content-Type": base.HeaderValue{"application/sdp"},
				"Content-Base": base.HeaderValue{"rtsp://localhost:8554/teststream/"},
			},
			Body: tracks.Write(false),
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Setup, req.Method)

		var inTH headers.Transport
		err = inTH.Read(req.Header["Transport"])
		require.NoError(t, err)

		th := headers.Transport{
			Delivery: func() *headers.TransportDelivery {
				v := headers.TransportDeliveryUnicast
				return &v
			}(),
			Protocol:       headers.TransportProtocolTCP,
			InterleavedIDs: inTH.InterleavedIDs,
		}

		base.Response{
			StatusCode: base.StatusOK,
			Header: base.Header{
				"Transport": th.Write(),
			},
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Play, req.Method)

		var ra headers.Range
		err = ra.Read(req.Header["Range"])
		require.NoError(t, err)
		require.Equal(t, headers.Range{
			Value: &headers.RangeNPT{
				Start: headers.RangeNPTTime(5500 * time.Millisecond),
			},
		}, ra)

		base.Response{
			StatusCode: base.StatusOK,
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Pause, req.Method)

		base.Response{
			StatusCode: base.StatusOK,
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Play, req.Method)

		err = ra.Read(req.Header["Range"])
		require.NoError(t, err)
		require.Equal(t, headers.Range{
			Value: &headers.RangeNPT{
				Start: headers.RangeNPTTime(6400 * time.Millisecond),
			},
		}, ra)

		base.Response{
			StatusCode: base.StatusOK,
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Teardown, req.Method)

		base.Response{
			StatusCode: base.StatusOK,
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)
	}()

	c := &Client{
		Transport: func() *Transport {
			v := TransportTCP
			return &v
		}(),
	}

	u, err := base.ParseURL("rtsp://localhost:8554/teststream")
	require.NoError(t, err)

	err = c.Start(u.Scheme, u.Host)
	require.NoError(t, err)
	defer c.Close()

	_, err = c.Options(u)
	require.NoError(t, err)

	tracks, baseURL, _, err := c.Describe(u)
	require.NoError(t, err)

	for _, track := range tracks {
		_, err := c.Setup(true, track, baseURL, 0, 0)
		require.NoError(t, err)
	}

	_, err = c.Play(&headers.Range{
		Value: &headers.RangeNPT{
			Start: headers.RangeNPTTime(5500 * time.Millisecond),
		},
	})
	require.NoError(t, err)

	_, err = c.Seek(&headers.Range{
		Value: &headers.RangeNPT{
			Start: headers.RangeNPTTime(6400 * time.Millisecond),
		},
	})
	require.NoError(t, err)
}

func TestClientReadKeepaliveFromSession(t *testing.T) {
	l, err := net.Listen("tcp", "localhost:8554")
	require.NoError(t, err)
	defer l.Close()

	keepaliveOk := make(chan struct{})

	serverDone := make(chan struct{})
	defer func() { <-serverDone }()
	go func() {
		defer close(serverDone)

		conn, err := l.Accept()
		require.NoError(t, err)
		defer conn.Close()
		br := bufio.NewReader(conn)
		var bb bytes.Buffer

		req, err := readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Options, req.Method)

		base.Response{
			StatusCode: base.StatusOK,
			Header: base.Header{
				"Public": base.HeaderValue{strings.Join([]string{
					string(base.Describe),
					string(base.Setup),
					string(base.Play),
				}, ", ")},
			},
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Describe, req.Method)

		track, err := NewTrackH264(96, []byte{0x01, 0x02, 0x03, 0x04}, []byte{0x01, 0x02, 0x03, 0x04}, nil)
		require.NoError(t, err)

		tracks := Tracks{track}
		tracks.setControls()

		base.Response{
			StatusCode: base.StatusOK,
			Header: base.Header{
				"Content-Type": base.HeaderValue{"application/sdp"},
				"Content-Base": base.HeaderValue{"rtsp://localhost:8554/teststream/"},
			},
			Body: tracks.Write(false),
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Setup, req.Method)

		var inTH headers.Transport
		err = inTH.Read(req.Header["Transport"])
		require.NoError(t, err)

		base.Response{
			StatusCode: base.StatusOK,
			Header: base.Header{
				"Transport": headers.Transport{
					Protocol: headers.TransportProtocolUDP,
					Delivery: func() *headers.TransportDelivery {
						v := headers.TransportDeliveryUnicast
						return &v
					}(),
					ClientPorts: inTH.ClientPorts,
					ServerPorts: &[2]int{34556, 34557},
				}.Write(),
				"Session": headers.Session{
					Session: "ABCDE",
					Timeout: func() *uint {
						v := uint(1)
						return &v
					}(),
				}.Write(),
			},
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		req, err = readRequest(br)
		require.NoError(t, err)
		require.Equal(t, base.Play, req.Method)

		base.Response{
			StatusCode: base.StatusOK,
		}.Write(&bb)
		_, err = conn.Write(bb.Bytes())
		require.NoError(t, err)

		recv := make(chan struct{})
		go func() {
			defer close(recv)
			req, err = readRequest(br)
			require.NoError(t, err)
			require.Equal(t, base.Options, req.Method)

			base.Response{
				StatusCode: base.StatusOK,
			}.Write(&bb)
			_, err = conn.Write(bb.Bytes())
			require.NoError(t, err)
		}()

		select {
		case <-recv:
		case <-time.After(3 * time.Second):
			t.Errorf("should not happen")
		}

		close(keepaliveOk)
	}()

	c := &Client{}

	err = c.StartReading("rtsp://localhost:8554/teststream")
	require.NoError(t, err)
	defer c.Close()

	<-keepaliveOk
}
