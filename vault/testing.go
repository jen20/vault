package vault

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/hashicorp/go-uuid"
	"github.com/hashicorp/vault/audit"
	"github.com/hashicorp/vault/helper/salt"
	"github.com/hashicorp/vault/logical"
	"github.com/hashicorp/vault/logical/framework"
	"github.com/hashicorp/vault/physical"
)

// This file contains a number of methods that are useful for unit
// tests within other packages.

const (
	testSharedPublicKey = `
ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQC9i+hFxZHGo6KblVme4zrAcJstR6I0PTJozW286X4WyvPnkMYDQ5mnhEYC7UWCvjoTWbPEXPX7NjhRtwQTGD67bV+lrxgfyzK1JZbUXK4PwgKJvQD+XyyWYMzDgGSQY61KUSqCxymSm/9NZkPU3ElaQ9xQuTzPpztM4ROfb8f2Yv6/ZESZsTo0MTAkp8Pcy+WkioI/uJ1H7zqs0EA4OMY4aDJRu0UtP4rTVeYNEAuRXdX+eH4aW3KMvhzpFTjMbaJHJXlEeUm2SaX5TNQyTOvghCeQILfYIL/Ca2ij8iwCmulwdV6eQGfd4VDu40PvSnmfoaE38o6HaPnX0kUcnKiT
`
	testSharedPrivateKey = `
-----BEGIN RSA PRIVATE KEY-----
MIIEogIBAAKCAQEAvYvoRcWRxqOim5VZnuM6wHCbLUeiND0yaM1tvOl+Fsrz55DG
A0OZp4RGAu1Fgr46E1mzxFz1+zY4UbcEExg+u21fpa8YH8sytSWW1FyuD8ICib0A
/l8slmDMw4BkkGOtSlEqgscpkpv/TWZD1NxJWkPcULk8z6c7TOETn2/H9mL+v2RE
mbE6NDEwJKfD3MvlpIqCP7idR+86rNBAODjGOGgyUbtFLT+K01XmDRALkV3V/nh+
GltyjL4c6RU4zG2iRyV5RHlJtkml+UzUMkzr4IQnkCC32CC/wmtoo/IsAprpcHVe
nkBn3eFQ7uND70p5n6GhN/KOh2j519JFHJyokwIDAQABAoIBAHX7VOvBC3kCN9/x
+aPdup84OE7Z7MvpX6w+WlUhXVugnmsAAVDczhKoUc/WktLLx2huCGhsmKvyVuH+
MioUiE+vx75gm3qGx5xbtmOfALVMRLopjCnJYf6EaFA0ZeQ+NwowNW7Lu0PHmAU8
Z3JiX8IwxTz14DU82buDyewO7v+cEr97AnERe3PUcSTDoUXNaoNxjNpEJkKREY6h
4hAY676RT/GsRcQ8tqe/rnCqPHNd7JGqL+207FK4tJw7daoBjQyijWuB7K5chSal
oPInylM6b13ASXuOAOT/2uSUBWmFVCZPDCmnZxy2SdnJGbsJAMl7Ma3MUlaGvVI+
Tfh1aQkCgYEA4JlNOabTb3z42wz6mz+Nz3JRwbawD+PJXOk5JsSnV7DtPtfgkK9y
6FTQdhnozGWShAvJvc+C4QAihs9AlHXoaBY5bEU7R/8UK/pSqwzam+MmxmhVDV7G
IMQPV0FteoXTaJSikhZ88mETTegI2mik+zleBpVxvfdhE5TR+lq8Br0CgYEA2AwJ
CUD5CYUSj09PluR0HHqamWOrJkKPFPwa+5eiTTCzfBBxImYZh7nXnWuoviXC0sg2
AuvCW+uZ48ygv/D8gcz3j1JfbErKZJuV+TotK9rRtNIF5Ub7qysP7UjyI7zCssVM
kuDd9LfRXaB/qGAHNkcDA8NxmHW3gpln4CFdSY8CgYANs4xwfercHEWaJ1qKagAe
rZyrMpffAEhicJ/Z65lB0jtG4CiE6w8ZeUMWUVJQVcnwYD+4YpZbX4S7sJ0B8Ydy
AhkSr86D/92dKTIt2STk6aCN7gNyQ1vW198PtaAWH1/cO2UHgHOy3ZUt5X/Uwxl9
cex4flln+1Viumts2GgsCQKBgCJH7psgSyPekK5auFdKEr5+Gc/jB8I/Z3K9+g4X
5nH3G1PBTCJYLw7hRzw8W/8oALzvddqKzEFHphiGXK94Lqjt/A4q1OdbCrhiE68D
My21P/dAKB1UYRSs9Y8CNyHCjuZM9jSMJ8vv6vG/SOJPsnVDWVAckAbQDvlTHC9t
O98zAoGAcbW6uFDkrv0XMCpB9Su3KaNXOR0wzag+WIFQRXCcoTvxVi9iYfUReQPi
oOyBJU/HMVvBfv4g+OVFLVgSwwm6owwsouZ0+D/LasbuHqYyqYqdyPJQYzWA2Y+F
+B6f4RoPdSXj24JHPg/ioRxjaj094UXJxua2yfkcecGNEuBQHSs=
-----END RSA PRIVATE KEY-----
`
)

// TestCore returns a pure in-memory, uninitialized core for testing.
func TestCore(t *testing.T) *Core {
	return TestCoreWithSeal(t, nil)
}

// TestCoreWithSeal returns a pure in-memory, uninitialized core with the
// specified seal for testing.
func TestCoreWithSeal(t *testing.T, testSeal Seal) *Core {
	noopAudits := map[string]audit.Factory{
		"noop": func(config *audit.BackendConfig) (audit.Backend, error) {
			view := &logical.InmemStorage{}
			view.Put(&logical.StorageEntry{
				Key:   "salt",
				Value: []byte("foo"),
			})
			var err error
			config.Salt, err = salt.NewSalt(view, &salt.Config{
				HMAC:     sha256.New,
				HMACType: "hmac-sha256",
			})
			if err != nil {
				t.Fatalf("error getting new salt: %v", err)
			}
			return &noopAudit{
				Config: config,
			}, nil
		},
	}
	noopBackends := make(map[string]logical.Factory)
	noopBackends["noop"] = func(config *logical.BackendConfig) (logical.Backend, error) {
		b := new(framework.Backend)
		b.Setup(config)
		return b, nil
	}
	noopBackends["http"] = func(config *logical.BackendConfig) (logical.Backend, error) {
		return new(rawHTTP), nil
	}
	logicalBackends := make(map[string]logical.Factory)
	for backendName, backendFactory := range noopBackends {
		logicalBackends[backendName] = backendFactory
	}
	logicalBackends["generic"] = LeasedPassthroughBackendFactory
	for backendName, backendFactory := range testLogicalBackends {
		logicalBackends[backendName] = backendFactory
	}

	logger := log.New(os.Stderr, "", log.LstdFlags)
	physicalBackend := physical.NewInmem(logger)
	conf := &CoreConfig{
		Physical:           physicalBackend,
		AuditBackends:      noopAudits,
		LogicalBackends:    logicalBackends,
		CredentialBackends: noopBackends,
		DisableMlock:       true,
		Logger:             logger,
	}
	if testSeal != nil {
		conf.Seal = testSeal
	}

	c, err := NewCore(conf)
	if err != nil {
		t.Fatalf("err: %s", err)
	}

	return c
}

// TestCoreInit initializes the core with a single key, and returns
// the key that must be used to unseal the core and a root token.
func TestCoreInit(t *testing.T, core *Core) ([]byte, string) {
	return TestCoreInitClusterListenerSetup(t, core, func() ([]net.Listener, http.Handler, error) { return nil, nil, nil })
}

func TestCoreInitClusterListenerSetup(t *testing.T, core *Core, setupFunc func() ([]net.Listener, http.Handler, error)) ([]byte, string) {
	core.SetClusterListenerSetupFunc(setupFunc)
	result, err := core.Initialize(&SealConfig{
		SecretShares:    1,
		SecretThreshold: 1,
	}, nil)
	if err != nil {
		t.Fatalf("err: %s", err)
	}
	return result.SecretShares[0], result.RootToken
}

func TestCoreUnseal(core *Core, key []byte) (bool, error) {
	core.SetClusterListenerSetupFunc(func() ([]net.Listener, http.Handler, error) { return nil, nil, nil })
	return core.Unseal(key)
}

// TestCoreUnsealed returns a pure in-memory core that is already
// initialized and unsealed.
func TestCoreUnsealed(t *testing.T) (*Core, []byte, string) {
	core := TestCore(t)
	key, token := TestCoreInit(t, core)
	if _, err := TestCoreUnseal(core, TestKeyCopy(key)); err != nil {
		t.Fatalf("unseal err: %s", err)
	}

	sealed, err := core.Sealed()
	if err != nil {
		t.Fatalf("err checking seal status: %s", err)
	}
	if sealed {
		t.Fatal("should not be sealed")
	}

	return core, key, token
}

// TestCoreWithTokenStore returns an in-memory core that has a token store
// mounted, so that logical token functions can be used
func TestCoreWithTokenStore(t *testing.T) (*Core, *TokenStore, []byte, string) {
	c, key, root := TestCoreUnsealed(t)

	me := &MountEntry{
		Table:       credentialTableType,
		Path:        "token/",
		Type:        "token",
		Description: "token based credentials",
	}

	meUUID, err := uuid.GenerateUUID()
	if err != nil {
		t.Fatal(err)
	}
	me.UUID = meUUID

	view := NewBarrierView(c.barrier, credentialBarrierPrefix+me.UUID+"/")

	tokenstore, _ := c.newCredentialBackend("token", c.mountEntrySysView(me), view, nil)
	ts := tokenstore.(*TokenStore)

	router := NewRouter()
	router.Mount(ts, "auth/token/", &MountEntry{Table: credentialTableType, UUID: ""}, ts.view)

	subview := c.systemBarrierView.SubView(expirationSubPath)
	logger := log.New(os.Stderr, "", log.LstdFlags)

	exp := NewExpirationManager(router, subview, ts, logger)
	ts.SetExpirationManager(exp)

	return c, ts, key, root
}

// TestKeyCopy is a silly little function to just copy the key so that
// it can be used with Unseal easily.
func TestKeyCopy(key []byte) []byte {
	result := make([]byte, len(key))
	copy(result, key)
	return result
}

var testLogicalBackends = map[string]logical.Factory{}

// Starts the test server which responds to SSH authentication.
// Used to test the SSH secret backend.
func StartSSHHostTestServer() (string, error) {
	pubKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(testSharedPublicKey))
	if err != nil {
		return "", fmt.Errorf("Error parsing public key")
	}
	serverConfig := &ssh.ServerConfig{
		PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if bytes.Compare(pubKey.Marshal(), key.Marshal()) == 0 {
				return &ssh.Permissions{}, nil
			} else {
				return nil, fmt.Errorf("Key does not match")
			}
		},
	}
	signer, err := ssh.ParsePrivateKey([]byte(testSharedPrivateKey))
	if err != nil {
		panic("Error parsing private key")
	}
	serverConfig.AddHostKey(signer)

	soc, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("Error listening to connection")
	}

	go func() {
		for {
			conn, err := soc.Accept()
			if err != nil {
				panic(fmt.Sprintf("Error accepting incoming connection: %s", err))
			}
			defer conn.Close()
			sshConn, chanReqs, _, err := ssh.NewServerConn(conn, serverConfig)
			if err != nil {
				panic(fmt.Sprintf("Handshaking error: %v", err))
			}

			go func() {
				for chanReq := range chanReqs {
					go func(chanReq ssh.NewChannel) {
						if chanReq.ChannelType() != "session" {
							chanReq.Reject(ssh.UnknownChannelType, "unknown channel type")
							return
						}

						ch, requests, err := chanReq.Accept()
						if err != nil {
							panic(fmt.Sprintf("Error accepting channel: %s", err))
						}

						go func(ch ssh.Channel, in <-chan *ssh.Request) {
							for req := range in {
								executeServerCommand(ch, req)
							}
						}(ch, requests)
					}(chanReq)
				}
				sshConn.Close()
			}()
		}
	}()
	return soc.Addr().String(), nil
}

// This executes the commands requested to be run on the server.
// Used to test the SSH secret backend.
func executeServerCommand(ch ssh.Channel, req *ssh.Request) {
	command := string(req.Payload[4:])
	cmd := exec.Command("/bin/bash", []string{"-c", command}...)
	req.Reply(true, nil)

	cmd.Stdout = ch
	cmd.Stderr = ch
	cmd.Stdin = ch

	err := cmd.Start()
	if err != nil {
		panic(fmt.Sprintf("Error starting the command: '%s'", err))
	}

	go func() {
		_, err := cmd.Process.Wait()
		if err != nil {
			panic(fmt.Sprintf("Error while waiting for command to finish:'%s'", err))
		}
		ch.Close()
	}()
}

// This adds a logical backend for the test core. This needs to be
// invoked before the test core is created.
func AddTestLogicalBackend(name string, factory logical.Factory) error {
	if name == "" {
		return fmt.Errorf("Missing backend name")
	}
	if factory == nil {
		return fmt.Errorf("Missing backend factory function")
	}
	testLogicalBackends[name] = factory
	return nil
}

type noopAudit struct {
	Config *audit.BackendConfig
}

func (n *noopAudit) GetHash(data string) string {
	return n.Config.Salt.GetIdentifiedHMAC(data)
}

func (n *noopAudit) LogRequest(a *logical.Auth, r *logical.Request, e error) error {
	return nil
}

func (n *noopAudit) LogResponse(a *logical.Auth, r *logical.Request, re *logical.Response, err error) error {
	return nil
}

type rawHTTP struct{}

func (n *rawHTTP) HandleRequest(req *logical.Request) (*logical.Response, error) {
	return &logical.Response{
		Data: map[string]interface{}{
			logical.HTTPStatusCode:  200,
			logical.HTTPContentType: "plain/text",
			logical.HTTPRawBody:     []byte("hello world"),
		},
	}, nil
}

func (n *rawHTTP) HandleExistenceCheck(req *logical.Request) (bool, bool, error) {
	return false, false, nil
}

func (n *rawHTTP) SpecialPaths() *logical.Paths {
	return &logical.Paths{Unauthenticated: []string{"*"}}
}

func (n *rawHTTP) System() logical.SystemView {
	return logical.StaticSystemView{
		DefaultLeaseTTLVal: time.Hour * 24,
		MaxLeaseTTLVal:     time.Hour * 24 * 30,
	}
}

func (n *rawHTTP) Cleanup() {
	// noop
}

func GenerateRandBytes(length int) ([]byte, error) {
	if length < 0 {
		return nil, fmt.Errorf("length must be >= 0")
	}

	buf := make([]byte, length)
	if length == 0 {
		return buf, nil
	}

	n, err := rand.Read(buf)
	if err != nil {
		return nil, err
	}
	if n != length {
		return nil, fmt.Errorf("unable to read %d bytes; only read %d", length, n)
	}

	return buf, nil
}

func TestWaitActive(t *testing.T, core *Core) {
	start := time.Now()
	var standby bool
	var err error
	for time.Now().Sub(start) < time.Second {
		standby, err = core.Standby()
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if !standby {
			break
		}
	}
	if standby {
		t.Fatalf("should not be in standby mode")
	}
}

type TestClusterCore struct {
	*Core
	Listeners []net.Listener
	Root      string
	Key       []byte
}

func (t *TestClusterCore) CloseListeners() {
	if t.Listeners != nil {
		for _, ln := range t.Listeners {
			ln.Close()
		}
	}
	// Give time to actually shut down/clean up before the next test
	time.Sleep(time.Second)
}

func TestCluster(t *testing.T, handlers []http.Handler, base *CoreConfig, unsealStandbys bool) []*TestClusterCore {
	if handlers == nil || len(handlers) != 3 {
		t.Fatal("handlers must be size 3")
	}

	logger := log.New(os.Stderr, "", log.LstdFlags)

	// Create three cores with the same physical and different advertise addrs
	coreConfig := &CoreConfig{
		Physical:           physical.NewInmem(logger),
		HAPhysical:         physical.NewInmemHA(logger),
		LogicalBackends:    make(map[string]logical.Factory),
		CredentialBackends: make(map[string]logical.Factory),
		AuditBackends:      make(map[string]audit.Factory),
		AdvertiseAddr:      "http://127.0.0.1:8202",
		ClusterAddr:        "https://127.0.0.1:8203",
		DisableMlock:       true,
	}

	// Used to set something non-working to test fallback
	if base.ClusterAddr != "" {
		coreConfig.ClusterAddr = base.ClusterAddr
	}

	coreConfig.LogicalBackends["generic"] = PassthroughBackendFactory

	if base != nil {
		if base.LogicalBackends != nil {
			for k, v := range base.LogicalBackends {
				coreConfig.LogicalBackends[k] = v
			}
		}
		if base.CredentialBackends != nil {
			for k, v := range base.CredentialBackends {
				coreConfig.CredentialBackends[k] = v
			}
		}
		if base.AuditBackends != nil {
			for k, v := range base.AuditBackends {
				coreConfig.AuditBackends[k] = v
			}
		}
	}

	c1, err := NewCore(coreConfig)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	coreConfig.AdvertiseAddr = "http://127.0.0.1:8206"
	coreConfig.ClusterAddr = "https://127.0.0.1:8207"
	c2, err := NewCore(coreConfig)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	coreConfig.AdvertiseAddr = "http://127.0.0.1:8208"
	coreConfig.ClusterAddr = "https://127.0.0.1:8209"
	c3, err := NewCore(coreConfig)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:8202")
	if err != nil {
		t.Fatal(err)
	}
	c1lns := []net.Listener{ln}
	ln, err = net.Listen("tcp", "127.0.0.1:8204")
	if err != nil {
		t.Fatal(err)
	}
	c1lns = append(c1lns, ln)
	server1 := &http.Server{
		Handler: handlers[0],
	}
	for _, ln := range c1lns {
		go server1.Serve(ln)
	}

	ln, err = net.Listen("tcp", "127.0.0.1:8206")
	if err != nil {
		t.Fatal(err)
	}
	c2lns := []net.Listener{ln}
	server2 := &http.Server{
		Handler: handlers[1],
	}
	for _, ln := range c2lns {
		go server2.Serve(ln)
	}

	ln, err = net.Listen("tcp", "127.0.0.1:8208")
	if err != nil {
		t.Fatal(err)
	}
	c3lns := []net.Listener{ln}
	server3 := &http.Server{
		Handler: handlers[2],
	}
	for _, ln := range c3lns {
		go server3.Serve(ln)
	}

	c2.SetClusterListenerSetupFunc(WrapListenersForClustering(c2lns, handlers[1], logger))
	c3.SetClusterListenerSetupFunc(WrapListenersForClustering(c3lns, handlers[2], logger))

	key, root := TestCoreInitClusterListenerSetup(t, c1, WrapListenersForClustering(c1lns, handlers[0], logger))
	if _, err := c1.Unseal(TestKeyCopy(key)); err != nil {
		t.Fatalf("unseal err: %s", err)
	}

	// Verify unsealed
	sealed, err := c1.Sealed()
	if err != nil {
		t.Fatalf("err checking seal status: %s", err)
	}
	if sealed {
		t.Fatal("should not be sealed")
	}

	TestWaitActive(t, c1)

	if unsealStandbys {
		if _, err := c2.Unseal(TestKeyCopy(key)); err != nil {
			t.Fatalf("unseal err: %s", err)
		}
		if _, err := c3.Unseal(TestKeyCopy(key)); err != nil {
			t.Fatalf("unseal err: %s", err)
		}

		// Let them come fully up to standby
		time.Sleep(2 * time.Second)

		// Ensure cluster connection info is populated
		isLeader, _, err := c2.Leader()
		if err != nil {
			t.Fatal(err)
		}
		if isLeader {
			t.Fatal("c2 should not be leader")
		}
		isLeader, _, err = c3.Leader()
		if err != nil {
			t.Fatal(err)
		}
		if isLeader {
			t.Fatal("c3 should not be leader")
		}
	}

	return []*TestClusterCore{
		&TestClusterCore{
			Core:      c1,
			Listeners: c1lns,
			Root:      root,
			Key:       TestKeyCopy(key),
		},
		&TestClusterCore{
			Core:      c2,
			Listeners: c2lns,
			Root:      root,
			Key:       TestKeyCopy(key),
		},
		&TestClusterCore{
			Core:      c3,
			Listeners: c3lns,
			Root:      root,
			Key:       TestKeyCopy(key),
		},
	}
}
