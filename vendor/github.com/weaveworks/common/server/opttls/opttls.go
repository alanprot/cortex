package opttls

import (
	"bytes"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/net/http2"
	"google.golang.org/grpc/credentials"
)

type listener struct {
	net.Listener
	config      *tls.Config
	connCounter *prometheus.CounterVec
}

func NewListener(l net.Listener, config *tls.Config, connCounter *prometheus.CounterVec) net.Listener {
	return &listener{l, config, connCounter}
}

type optTLSErr struct {
	err string
}

func (e optTLSErr) Error() string   { return e.err }
func (e optTLSErr) Timeout() bool   { return false }
func (e optTLSErr) Temporary() bool { return true }

var _ net.Error = optTLSErr{}

func (l *listener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}

	c, isTLS, err := PeekTLS(c)
	l.connCounter.WithLabelValues("http", strconv.FormatBool(isTLS)).Inc()
	if err != nil {
		return nil, err
	}

	if isTLS {
		return tls.Server(c, l.config), nil
	}
	return c, nil
}

type peekedConn struct {
	net.Conn
	r io.Reader
}

func (c *peekedConn) Read(b []byte) (int, error) {
	return c.r.Read(b)
}

func PeekTLS(c net.Conn) (net.Conn, bool, error) {
	b := make([]byte, 2)
	n, err := c.Read(b)
	if err != nil {
		cerr := c.Close()
		if cerr != nil {
			return nil, false, err
		}
		return nil, false, optTLSErr{err.Error()}
	}
	if n < 2 {
		err := c.Close()
		if err != nil {
			return nil, false, err
		}
		return nil, false, optTLSErr{"EOF"}
	}

	tlsFrameType := b[0]
	tlsMajorVersion := b[1]
	//isTLS := (tlsFrameType == TLS_HANDSHAKE_FRAME_TYPE || tlsFrameType == TLS_ALERT_FRAME_TYPE) && tlsMajorVersion == TLS_MAJOR_VERSION;
	isTLS := (tlsFrameType == 0x15 || tlsFrameType == 0x16) && tlsMajorVersion == 3
	c = &peekedConn{c, io.MultiReader(bytes.NewBuffer(b), c)}
	return c, isTLS, nil
}
func ServeTLSOptionally(srv *http.Server, l net.Listener, certFile, keyFile string, connCounter *prometheus.CounterVec) error {
	err := http2.ConfigureServer(srv, nil)
	if err != nil {
		return err
	}

	config := cloneTLSConfig(srv.TLSConfig)
	if !strSliceContains(config.NextProtos, "http/1.1") {
		config.NextProtos = append(config.NextProtos, "http/1.1")
	}

	configHasCert := len(config.Certificates) > 0 || config.GetCertificate != nil
	if !configHasCert || certFile != "" || keyFile != "" {
		var err error
		config.Certificates = make([]tls.Certificate, 1)
		config.Certificates[0], err = tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return err
		}
	}

	return srv.Serve(NewListener(l, config, connCounter))
}

func cloneTLSConfig(cfg *tls.Config) *tls.Config {
	if cfg == nil {
		return &tls.Config{}
	}
	return cfg.Clone()
}

func strSliceContains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

type OptionalCred struct {
	credentials.TransportCredentials
	connCounter *prometheus.CounterVec
	Registerer  prometheus.Registerer
}

func NewTlsOptionalCred(cred credentials.TransportCredentials, connCounter *prometheus.CounterVec) *OptionalCred {
	return &OptionalCred{
		TransportCredentials: cred,
		connCounter:          connCounter,
	}
}

func (c *OptionalCred) ServerHandshake(rawConn net.Conn) (net.Conn, credentials.AuthInfo, error) {
	rawConn, isTLS, err := PeekTLS(rawConn)
	c.connCounter.WithLabelValues("grpc", strconv.FormatBool(isTLS)).Inc()
	if err != nil {
		return nil, nil, err
	}
	if isTLS {
		return c.TransportCredentials.ServerHandshake(rawConn)
	}
	return rawConn, nil, nil
}
