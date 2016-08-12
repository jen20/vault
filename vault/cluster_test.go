package vault

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/hashicorp/vault/helper/jsonutil"
	"github.com/hashicorp/vault/logical"
	"github.com/hashicorp/vault/physical"
)

func TestClusterFetching(t *testing.T) {
	c, _, _ := TestCoreUnsealed(t)

	err := c.setupCluster()
	if err != nil {
		t.Fatal(err)
	}

	cluster, err := c.Cluster()
	if err != nil {
		t.Fatal(err)
	}
	// Test whether expected values are found
	if cluster == nil || cluster.Name == "" || cluster.ID == "" {
		t.Fatalf("cluster information missing: cluster: %#v", cluster)
	}

	// Test whether a private key has been generated
	entry, err := c.barrier.Get(coreLocalClusterKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	if entry == nil {
		t.Fatal("missing local cluster private key")
	}

	var params clusterKeyParams
	if err = jsonutil.DecodeJSON(entry.Value, &params); err != nil {
		t.Fatal(err)
	}
	switch {
	case params.X == nil, params.Y == nil, params.D == nil:
		t.Fatalf("x or y or d are nil: %#v", params)
	case params.Type == corePrivateKeyTypeP521:
	default:
		t.Fatal("parameter error: %#v", params)
	}
}

func TestClusterHAFetching(t *testing.T) {
	logger = log.New(os.Stderr, "", log.LstdFlags)
	advertise := "http://127.0.0.1:8200"

	c, err := NewCore(&CoreConfig{
		Physical:      physical.NewInmemHA(logger),
		HAPhysical:    physical.NewInmemHA(logger),
		AdvertiseAddr: advertise,
		DisableMlock:  true,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	key, _ := TestCoreInit(t, c)
	if _, err := TestCoreUnseal(c, TestKeyCopy(key)); err != nil {
		t.Fatalf("unseal err: %s", err)
	}

	// Verify unsealed
	sealed, err := c.Sealed()
	if err != nil {
		t.Fatalf("err checking seal status: %s", err)
	}
	if sealed {
		t.Fatal("should not be sealed")
	}

	// Wait for core to become active
	TestWaitActive(t, c)

	cluster, err := c.Cluster()
	if err != nil {
		t.Fatal(err)
	}
	// Test whether expected values are found
	if cluster == nil || cluster.Name == "" || cluster.ID == "" || cluster.Certificate == nil || len(cluster.Certificate) == 0 {
		t.Fatalf("cluster information missing: cluster:%#v", cluster)
	}

	// Test whether a private key has been generated
	entry, err := c.barrier.Get(coreLocalClusterKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	if entry == nil {
		t.Fatal("missing local cluster private key")
	}

	var params clusterKeyParams
	if err = jsonutil.DecodeJSON(entry.Value, &params); err != nil {
		t.Fatal(err)
	}
	switch {
	case params.X == nil, params.Y == nil, params.D == nil:
		t.Fatalf("x or y or d are nil: %#v", params)
	case params.Type == corePrivateKeyTypeP521:
	default:
		t.Fatal("parameter error: %#v", params)
	}

	// Make sure the certificate meets expectations
	_, err = x509.ParseCertificate(cluster.Certificate)
	if err != nil {
		t.Fatal("error parsing local cluster certificate: %v", err)
	}
}

func TestCluster_ListenForRequests(t *testing.T) {
	cores := TestCluster(t, []http.Handler{nil, nil, nil}, nil, false)
	for _, core := range cores {
		defer core.CloseListeners()
	}

	root := cores[0].Root

	// Make this nicer for tests
	oldManualStepDownSleepPeriod := manualStepDownSleepPeriod
	manualStepDownSleepPeriod = 5 * time.Second
	// Restore this value for other tests
	defer func() { manualStepDownSleepPeriod = oldManualStepDownSleepPeriod }()

	// Wait for core to become active
	TestWaitActive(t, cores[0].Core)

	tlsConfig, err := cores[0].ClusterTLSConfig()
	if err != nil {
		t.Fatal(err)
	}

	checkListenersFunc := func(expectFail bool) {
		for _, ln := range cores[0].Listeners {
			tcpAddr, ok := ln.Addr().(*net.TCPAddr)
			if !ok {
				t.Fatal("%s not a TCP port", tcpAddr.String())
			}

			conn, err := tls.Dial("tcp", fmt.Sprintf("%s:%d", tcpAddr.IP.String(), tcpAddr.Port+1), tlsConfig)
			if err != nil {
				if expectFail {
					t.Logf("testing %s:%d unsuccessful as expected", tcpAddr.IP.String(), tcpAddr.Port+1)
					continue
				}
				t.Fatalf("error: %v\nlisteners are\n%#v\n%#v\n", err, cores[0].Listeners[0], cores[0].Listeners[1])
			}
			if expectFail {
				t.Fatalf("testing %s:%d not unsuccessful as expected", tcpAddr.IP.String(), tcpAddr.Port+1)
			}
			err = conn.Handshake()
			if err != nil {
				t.Fatal(err)
			}
			connState := conn.ConnectionState()
			switch {
			case connState.Version != tls.VersionTLS12:
				t.Fatal("version mismatch")
			case connState.NegotiatedProtocol != "h2" || !connState.NegotiatedProtocolIsMutual:
				t.Fatal("bad protocol negotiation")
			}
			t.Logf("testing %s:%d successful", tcpAddr.IP.String(), tcpAddr.Port+1)
		}
	}

	checkListenersFunc(false)

	err = cores[0].StepDown(&logical.Request{
		Operation:   logical.UpdateOperation,
		Path:        "sys/step-down",
		ClientToken: root,
	})
	if err != nil {
		t.Fatal(err)
	}

	// StepDown doesn't wait during actual preSeal so give time for listeners
	// to close
	time.Sleep(1 * time.Second)
	checkListenersFunc(true)

	// After this period it should be active again
	time.Sleep(manualStepDownSleepPeriod)
	checkListenersFunc(false)

	err = cores[0].Seal(root)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1 * time.Second)
	// After sealing it should be inactive again
	checkListenersFunc(true)
}

func TestCluster_ForwardRequests(t *testing.T) {
	handler1 := http.NewServeMux()
	handler1.HandleFunc("/core1", func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(201)
		w.Write([]byte("core1"))
	})
	handler2 := http.NewServeMux()
	handler2.HandleFunc("/core2", func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(202)
		w.Write([]byte("core2"))
	})
	handler3 := http.NewServeMux()
	handler3.HandleFunc("/core3", func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(203)
		w.Write([]byte("core3"))
	})

	cores := TestCluster(t, []http.Handler{handler1, handler2, handler3}, nil, true)
	for _, core := range cores {
		defer core.CloseListeners()
	}

	root := cores[0].Root

	// Make this nicer for tests
	oldManualStepDownSleepPeriod := manualStepDownSleepPeriod
	manualStepDownSleepPeriod = 5 * time.Second
	// Restore this value for other tests
	defer func() { manualStepDownSleepPeriod = oldManualStepDownSleepPeriod }()

	// Wait for core to become active
	TestWaitActive(t, cores[0].Core)

	// Test forwarding a request. Since we're going directly from core to core
	// with no fallback we know that if it worked, request handling is working
	testCluster_ForwardRequests(t, cores[1], "core1")
	testCluster_ForwardRequests(t, cores[2], "core1")

	//
	// Now we do a bunch of round-robining. The point is to make sure that as
	// nodes come and go, we can always successfully forward to the active
	// node.
	//

	// Ensure active core is cores[1] and test
	err := cores[0].StepDown(&logical.Request{
		Operation:   logical.UpdateOperation,
		Path:        "sys/step-down",
		ClientToken: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	_ = cores[2].StepDown(&logical.Request{
		Operation:   logical.UpdateOperation,
		Path:        "sys/step-down",
		ClientToken: root,
	})
	time.Sleep(2 * time.Second)
	TestWaitActive(t, cores[1].Core)
	testCluster_ForwardRequests(t, cores[0], "core2")
	testCluster_ForwardRequests(t, cores[2], "core2")

	// Ensure active core is cores[2] and test
	err = cores[1].StepDown(&logical.Request{
		Operation:   logical.UpdateOperation,
		Path:        "sys/step-down",
		ClientToken: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	_ = cores[0].StepDown(&logical.Request{
		Operation:   logical.UpdateOperation,
		Path:        "sys/step-down",
		ClientToken: root,
	})
	time.Sleep(2 * time.Second)
	TestWaitActive(t, cores[2].Core)
	testCluster_ForwardRequests(t, cores[0], "core3")
	testCluster_ForwardRequests(t, cores[1], "core3")

	// Ensure active core is cores[0] and test
	err = cores[2].StepDown(&logical.Request{
		Operation:   logical.UpdateOperation,
		Path:        "sys/step-down",
		ClientToken: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	_ = cores[1].StepDown(&logical.Request{
		Operation:   logical.UpdateOperation,
		Path:        "sys/step-down",
		ClientToken: root,
	})
	time.Sleep(2 * time.Second)
	TestWaitActive(t, cores[0].Core)
	testCluster_ForwardRequests(t, cores[1], "core1")
	testCluster_ForwardRequests(t, cores[2], "core1")

	// Ensure active core is cores[1] and test
	err = cores[0].StepDown(&logical.Request{
		Operation:   logical.UpdateOperation,
		Path:        "sys/step-down",
		ClientToken: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	_ = cores[2].StepDown(&logical.Request{
		Operation:   logical.UpdateOperation,
		Path:        "sys/step-down",
		ClientToken: root,
	})
	time.Sleep(2 * time.Second)
	TestWaitActive(t, cores[1].Core)
	testCluster_ForwardRequests(t, cores[0], "core2")
	testCluster_ForwardRequests(t, cores[2], "core2")

	// Ensure active core is cores[2] and test
	err = cores[1].StepDown(&logical.Request{
		Operation:   logical.UpdateOperation,
		Path:        "sys/step-down",
		ClientToken: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	_ = cores[0].StepDown(&logical.Request{
		Operation:   logical.UpdateOperation,
		Path:        "sys/step-down",
		ClientToken: root,
	})
	time.Sleep(2 * time.Second)
	TestWaitActive(t, cores[2].Core)
	testCluster_ForwardRequests(t, cores[0], "core3")
	testCluster_ForwardRequests(t, cores[1], "core3")
}

func testCluster_ForwardRequests(t *testing.T, c *TestClusterCore, remoteCoreID string) {
	standby, err := c.Standby()
	if err != nil {
		t.Fatal(err)
	}
	if !standby {
		t.Fatal("expected core to be standby")
	}

	// We need to call Leader as that refreshes the connection info
	isLeader, _, err := c.Leader()
	if err != nil {
		t.Fatal(err)
	}
	if isLeader {
		t.Fatal("core should not be leader")
	}

	bodBuf := bytes.NewReader([]byte(`{ "foo": "bar", "zip": "zap" }`))
	req, err := http.NewRequest("PUT", "https://pushit.real.good:9281/"+remoteCoreID, bodBuf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Add("X-Vault-Token", c.Root)

	resp, err := c.ForwardRequest(req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp == nil {
		t.Fatal("nil resp")
	}
	defer resp.Body.Close()

	body := bytes.NewBuffer(nil)
	body.ReadFrom(resp.Body)

	if body.String() != remoteCoreID {
		t.Fatalf("expected %s, got %s", remoteCoreID, body.String())
	}
	switch body.String() {
	case "core1":
		if resp.StatusCode != 201 {
			t.Fatal("bad response")
		}
	case "core2":
		if resp.StatusCode != 202 {
			t.Fatal("bad response")
		}
	case "core3":
		if resp.StatusCode != 203 {
			t.Fatal("bad response")
		}
	}
}
