package vault

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	mathrand "math/rand"
	"net"
	"net/http"
	"time"

	"golang.org/x/net/http2"

	"github.com/hashicorp/go-uuid"
	"github.com/hashicorp/vault/helper/jsonutil"
	"github.com/hashicorp/vault/helper/requestutil"
)

const (
	// Storage path where the local cluster name and identifier are stored
	coreLocalClusterInfoPath = "core/cluster/local/info"
	coreLocalClusterKeyPath  = "core/cluster/local/key"

	corePrivateKeyTypeP521 = "p521"
)

var (
	ErrCannotForward = errors.New("cannot forward request; no connection or address not known")
)

type privKeyParams struct {
	Type string   `json:"type"`
	X    *big.Int `json:"x"`
	Y    *big.Int `json:"y"`
	D    *big.Int `json:"d"`
}

type activeConnection struct {
	*http.Client
	clusterAddr string
}

// Structure representing the storage entry that holds cluster information
type Cluster struct {
	// Name of the cluster
	Name string `json:"name" structs:"name" mapstructure:"name"`

	// Identifier of the cluster
	ID string `json:"id" structs:"id" mapstructure:"id"`

	// Certificate corresponding to the private key
	Certificate []byte `json:"certificate" structs:"certificate" mapstructure:"certificate"`
}

// Cluster fetches the details of either local or global cluster based on the
// input. This method errors out when Vault is sealed.
func (c *Core) Cluster() (*Cluster, error) {
	var cluster Cluster

	// Fetch the storage entry. This call fails when Vault is sealed.
	entry, err := c.barrier.Get(coreLocalClusterInfoPath)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return &cluster, nil
	}

	// Decode the cluster information
	if err = jsonutil.DecodeJSON(entry.Value, &cluster); err != nil {
		return nil, fmt.Errorf("failed to decode cluster details: %v", err)
	}

	// Set in config file
	if c.clusterName != "" {
		cluster.Name = c.clusterName
	}

	if c.localClusterCertPool == nil {
		c.localClusterCertPool = x509.NewCertPool()
	}
	if cluster.Certificate != nil && len(cluster.Certificate) != 0 {
		cert, err := x509.ParseCertificate(cluster.Certificate)
		if err != nil {
			return nil, fmt.Errorf("error parsing local cluster certificate: %v", err)
		}

		// This is idempotent
		c.localClusterCertPool.AddCert(cert)
	}

	return &cluster, nil
}

// setupCluster creates storage entries for holding Vault cluster information.
// Entries will be created only if they are not already present. If clusterName
// is not supplied, this method will auto-generate it.
func (c *Core) setupCluster() error {
	// Check if storage index is already present or not
	cluster, err := c.Cluster()
	if err != nil {
		c.logger.Printf("[ERR] core: failed to get cluster details: %v", err)
		return err
	}

	var modified bool
	var needNewCert bool

	if cluster == nil {
		cluster = &Cluster{}
	}

	if cluster.Name == "" {
		// If cluster name is not supplied, generate one
		if c.clusterName == "" {
			c.logger.Printf("[TRACE] core: cluster name not found/set, generating new")
			clusterNameBytes, err := uuid.GenerateRandomBytes(4)
			if err != nil {
				c.logger.Printf("[ERR] core: failed to generate cluster name: %v", err)
				return err
			}

			c.clusterName = fmt.Sprintf("vault-cluster-%08x", clusterNameBytes)
		}

		cluster.Name = c.clusterName
		c.logger.Printf("[DEBUG] core: cluster name set to %s", cluster.Name)
		modified = true
	}

	if cluster.ID == "" {
		c.logger.Printf("[TRACE] core: cluster ID not found, generating new")
		// Generate a clusterID
		cluster.ID, err = uuid.GenerateUUID()
		if err != nil {
			c.logger.Printf("[ERR] core: failed to generate cluster identifier: %v", err)
			return err
		}
		c.logger.Printf("[DEBUG] core: cluster ID set to %s", cluster.ID)
		modified = true
	}

	// Check for and optionally generate the local cluster private key
	if c.localClusterPrivateKey == nil {
		c.logger.Printf("[TRACE] core: local cluster private key not loaded, loading now")
		entry, err := c.barrier.Get(coreLocalClusterKeyPath)
		if err != nil {
			c.logger.Printf("[ERR] core: failed to read local cluster key path: %v", err)
			return err
		}
		if entry == nil {
			c.logger.Printf("[TRACE] core: local cluster private key not found, generating new")
			key, err := ecdsa.GenerateKey(elliptic.P521(), rand.Reader)
			if err != nil {
				c.logger.Printf("[ERR] core: failed to generate local cluster key: %v", err)
				return err
			}

			// Encode the cluster information into as a JSON string
			rawPrivKeyParams, err := json.Marshal(&privKeyParams{
				Type: corePrivateKeyTypeP521,
				X:    key.X,
				Y:    key.Y,
				D:    key.D,
			})
			if err != nil {
				c.logger.Printf("[ERR] core: failed to encode local cluster key: %v", err)
				return err
			}

			// Store it
			err = c.barrier.Put(&Entry{
				Key:   coreLocalClusterKeyPath,
				Value: rawPrivKeyParams,
			})
			if err != nil {
				c.logger.Printf("[ERR] core: failed to store local cluster key: %v", err)
				return err
			}

			needNewCert = true
			c.localClusterPrivateKey = key
		} else {
			c.logger.Printf("[TRACE] core: local cluster private key found")
			var params privKeyParams
			if err = jsonutil.DecodeJSON(entry.Value, &params); err != nil {
				c.logger.Printf("[ERR] core: failed to decode local cluster key: %v", err)
				return err
			}
			switch {
			case params.X == nil, params.Y == nil, params.D == nil:
				c.logger.Printf("[ERR] core: failed to parse local cluster key: %v", err)
				return fmt.Errorf("failed to parse local cluster key")
			case params.Type == corePrivateKeyTypeP521:
			default:
				c.logger.Printf("[ERR] core: unknown local cluster key type %v", params.Type)
				return fmt.Errorf("failed to find valid local cluster key type")
			}
			c.localClusterPrivateKey = &ecdsa.PrivateKey{
				PublicKey: ecdsa.PublicKey{
					Curve: elliptic.P521(),
					X:     params.X,
					Y:     params.Y,
				},
				D: params.D,
			}
		}
	}

	if (needNewCert || cluster.Certificate == nil || len(cluster.Certificate) == 0) && c.advertiseAddr != "" {
		c.logger.Printf("[TRACE] core: generating new local cluster certificate")
		if c.localClusterPrivateKey == nil {
			c.logger.Printf("[ERR] core: generating a new local cluster certificate but private key is nil")
			return fmt.Errorf("failed to find private key when generating new local cluster certificate")
		}

		host, err := uuid.GenerateUUID()
		if err != nil {
			return err
		}

		template := &x509.Certificate{
			Subject: pkix.Name{
				CommonName: host,
			},
			DNSNames: []string{host},
			ExtKeyUsage: []x509.ExtKeyUsage{
				x509.ExtKeyUsageServerAuth,
				x509.ExtKeyUsageClientAuth,
			},
			KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageKeyAgreement | x509.KeyUsageCertSign,
			SerialNumber:          big.NewInt(mathrand.Int63()),
			NotBefore:             time.Now().Add(-30 * time.Second),
			NotAfter:              time.Now().Add(262980 * time.Hour),
			BasicConstraintsValid: true,
			IsCA: true,
		}

		certBytes, err := x509.CreateCertificate(rand.Reader, template, template, c.localClusterPrivateKey.Public(), c.localClusterPrivateKey)
		if err != nil {
			c.logger.Printf("[ERR] core: error generating self-signed cert: %v", err)
			return fmt.Errorf("unable to generate local cluster certificate: %v", err)
		}

		_, err = x509.ParseCertificate(certBytes)
		if err != nil {
			c.logger.Printf("[ERR] core: error parsing self-signed cert: %v", err)
			return fmt.Errorf("error parsing generated certificate: %v", err)
		}

		cluster.Certificate = certBytes
		modified = true
	}

	if modified {
		// Encode the cluster information into as a JSON string
		rawCluster, err := json.Marshal(cluster)
		if err != nil {
			c.logger.Printf("[ERR] core: failed to encode cluster details: %v", err)
			return err
		}

		// Store it
		err = c.barrier.Put(&Entry{
			Key:   coreLocalClusterInfoPath,
			Value: rawCluster,
		})
		if err != nil {
			c.logger.Printf("[ERR] core: failed to store cluster details: %v", err)
			return err
		}
	}

	return nil
}

// SetClusterListenerSetupFunc sets the listener setup func, which is used to
// know which ports to listen on and a handler to use.
func (c *Core) SetClusterListenerSetupFunc(setupFunc func() ([]net.Listener, http.Handler, error)) {
	c.clusterListenerSetupFunc = setupFunc
}

// startClusterListener starts cluster request listeners during postunseal. It
// is assumed that the state lock is held while this is run.
func (c *Core) startClusterListener() error {
	if c.clusterListenerShutdownCh != nil {
		c.logger.Printf("[ERR] core/startClusterListener: attempt to set up cluster listeners when already set up")
		return fmt.Errorf("cluster listeners already setup")
	}

	if c.clusterListenerSetupFunc == nil {
		c.logger.Printf("[ERR] core/startClusterListener: cluster listener setup function has not been set")
		return fmt.Errorf("cluster listener setup function has not been set")
	}

	lns, handler, err := c.clusterListenerSetupFunc()
	if err != nil {
		return err
	}

	tlsConfig, err := c.ClusterTLSConfig()
	if err != nil {
		c.logger.Printf("[ERR] core/startClusterListener: failed to get tls configuration: %v", err)
		return err
	}

	tlsLns := make([]net.Listener, 0, len(lns))
	for _, ln := range lns {
		tlsLn := tls.NewListener(ln, tlsConfig)
		tlsLns = append(tlsLns, tlsLn)
		server := &http.Server{
			Handler: handler,
		}
		http2.ConfigureServer(server, nil)
		c.logger.Printf("[TRACE] core/startClusterListener: serving cluster requests on %s", tlsLn.Addr())
		go server.Serve(tlsLn)
	}

	c.clusterListenerShutdownCh = make(chan struct{})
	c.clusterListenerShutdownSuccessCh = make(chan struct{})

	go func() {
		<-c.clusterListenerShutdownCh
		c.logger.Printf("[TRACE] core/startClusterListener: shutting down listeners")
		for _, tlsLn := range tlsLns {
			tlsLn.Close()
		}
		close(c.clusterListenerShutdownSuccessCh)
	}()

	return nil
}

// stopClusterListener stops any existing listeners during preseal. It is
// assumed that the state lock is held while this is run.
func (c *Core) stopClusterListener() {
	if c.clusterListenerShutdownCh != nil {
		close(c.clusterListenerShutdownCh)
		defer func() { c.clusterListenerShutdownCh = nil }()
	}

	// The reason for this loop-de-loop is that we may be unsealing again
	// quickly, and if the listeners are not yet closed, we will get socket
	// bind errors. This ensures proper ordering.
	if c.clusterListenerShutdownSuccessCh == nil {
		return
	}
	<-c.clusterListenerShutdownSuccessCh
	defer func() { c.clusterListenerShutdownSuccessCh = nil }()
}

// ClusterTLSConfig generates a TLS configuration based on the local cluster
// key and cert.
func (c *Core) ClusterTLSConfig() (*tls.Config, error) {
	cluster, err := c.Cluster()
	if err != nil {
		return nil, err
	}
	if cluster == nil {
		return nil, fmt.Errorf("cluster information is nil")
	}
	if cluster.Certificate == nil || len(cluster.Certificate) == 0 {
		return nil, fmt.Errorf("cluster certificate is nil")
	}

	parsedCert, err := x509.ParseCertificate(cluster.Certificate)
	if err != nil {
		return nil, fmt.Errorf("error parsing local cluster certificate: %v", err)
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{
			tls.Certificate{
				Certificate: [][]byte{cluster.Certificate},
				PrivateKey:  c.localClusterPrivateKey,
			},
		},
		RootCAs:    c.localClusterCertPool,
		ServerName: parsedCert.Subject.CommonName,
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  c.localClusterCertPool,
		NextProtos: []string{
			"h2",
		},
	}

	return tlsConfig, nil
}

// refreshRequestForwardingConnection ensures that the client/transport are
// alive and that the current active address value matches the most
// recently-known address.
func (c *Core) refreshRequestForwardingConnection(clusterAddr string) error {
	c.requestForwardingConnectionWriteLock.RLock()

	if c.requestForwardingConnection == nil && clusterAddr == "" {
		c.requestForwardingConnectionWriteLock.RUnlock()
		return nil
	}
	if c.requestForwardingConnection != nil &&
		c.requestForwardingConnection.clusterAddr == clusterAddr {
		c.requestForwardingConnectionWriteLock.RUnlock()
		return nil
	}

	// Give up the read lock and get a write lock, then verify it's still required
	c.requestForwardingConnectionWriteLock.RUnlock()
	c.requestForwardingConnectionWriteLock.Lock()
	defer c.requestForwardingConnectionWriteLock.Unlock()
	if c.requestForwardingConnection != nil &&
		c.requestForwardingConnection.clusterAddr == clusterAddr {
		return nil
	}

	// Disabled, potentially
	if clusterAddr == "" {
		c.requestForwardingConnection = nil
		return nil
	}

	if c.requestForwardingConnection == nil {
		tlsConfig, err := c.ClusterTLSConfig()
		if err != nil {
			return err
		}
		tp := &http.Transport{
			TLSClientConfig: tlsConfig,
		}
		err = http2.ConfigureTransport(tp)
		if err != nil {
			return err
		}
		c.requestForwardingConnection = &activeConnection{
			Client: &http.Client{
				Transport: tp,
			},
		}
	}

	if c.requestForwardingConnection.clusterAddr != clusterAddr {
		c.requestForwardingConnection.clusterAddr = clusterAddr
	}

	return nil
}

// ForwardRequest forwards a given request to the active node and returns the
// response.
func (c *Core) ForwardRequest(req *http.Request) (*http.Response, error) {
	c.requestForwardingConnectionWriteLock.RLock()
	defer c.requestForwardingConnectionWriteLock.RUnlock()
	if c.requestForwardingConnection == nil {
		return nil, ErrCannotForward
	}

	if c.requestForwardingConnection.clusterAddr == "" {
		return nil, ErrCannotForward
	}

	freq, err := requestutil.GenerateForwardedRequest(req, c.requestForwardingConnection.clusterAddr)
	if err != nil {
		return nil, err
	}

	return c.requestForwardingConnection.Do(freq)
}
