package cluster

import (
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/quilt/quilt/cluster/acl"
	"github.com/quilt/quilt/cluster/cloudcfg"
	"github.com/quilt/quilt/cluster/machine"
	"github.com/quilt/quilt/db"
	"github.com/quilt/quilt/join"
	"github.com/stretchr/testify/assert"
)

var FakeAmazon db.Provider = "FakeAmazon"
var FakeVagrant db.Provider = "FakeVagrant"
var amazonCloudConfig = "Amazon Cloud Config"
var vagrantCloudConfig = "Vagrant Cloud Config"
var testRegion = "Fake region"

type providerRequest struct {
	request  machine.Machine
	provider provider
	boot     bool
}

type bootRequest struct {
	size        string
	cloudConfig string
	role        db.Role
}

type ipRequest struct {
	size        string
	cloudConfig string
	ip          string
}

type fakeProvider struct {
	namespace   string
	machines    map[string]machine.Machine
	roles       map[string]db.Role
	idCounter   int
	cloudConfig string

	bootRequests []bootRequest
	stopRequests []string
	updateIPs    []ipRequest
	aclRequests  []acl.ACL

	listError error
}

func fakeValidRegions(p db.Provider) []string {
	return []string{testRegion}
}

func (p *fakeProvider) clearLogs() {
	p.bootRequests = []bootRequest{}
	p.stopRequests = []string{}
	p.aclRequests = []acl.ACL{}
	p.updateIPs = []ipRequest{}
}

func (p *fakeProvider) List() ([]machine.Machine, error) {
	if p.listError != nil {
		return nil, p.listError
	}

	var machines []machine.Machine
	for _, machine := range p.machines {
		machines = append(machines, machine)
	}
	return machines, nil
}

func (p *fakeProvider) Boot(bootSet []machine.Machine) error {
	for _, toBoot := range bootSet {
		p.idCounter++
		idStr := strconv.Itoa(p.idCounter)
		toBoot.ID = idStr
		toBoot.PublicIP = idStr

		// A machine's role is `None` until the minion boots, at which
		// `getMachineRoles` will populate this field with the correct role.
		// We simulate this by setting the role of the machine returned by
		// `List()` to be None, and only return the correct role in
		// `getMachineRole`.
		p.roles[toBoot.PublicIP] = toBoot.CloudCfgOpts.MinionOpts.Role
		toBoot.CloudCfgOpts.MinionOpts.Role = db.None

		p.machines[idStr] = toBoot
		p.bootRequests = append(p.bootRequests, bootRequest{size: toBoot.Size,
			cloudConfig: p.cloudConfig})
	}

	return nil
}

func (p *fakeProvider) Stop(machines []machine.Machine) error {
	for _, machine := range machines {
		delete(p.machines, machine.ID)
		p.stopRequests = append(p.stopRequests, machine.ID)
	}
	return nil
}

func (p *fakeProvider) SetACLs(acls []acl.ACL) error {
	p.aclRequests = acls
	return nil
}

func (p *fakeProvider) UpdateFloatingIPs(machines []machine.Machine) error {
	for _, m := range machines {
		p.updateIPs = append(p.updateIPs, ipRequest{
			size:        m.Size,
			cloudConfig: p.cloudConfig,
			ip:          m.FloatingIP,
		})

		p.machines[m.ID] = m
	}

	return nil
}

func newTestCluster(namespace string) *cluster {
	sleep = func(t time.Duration) {}
	mock()
	return newCluster(db.New(), namespace)
}

func TestPanicBadProvider(t *testing.T) {
	temp := allProviders
	defer func() {
		r := recover()
		assert.NotNil(t, r)
		allProviders = temp
	}()
	allProviders = []db.Provider{FakeAmazon}
	conn := db.New()
	newCluster(conn, "test")
}

func TestSyncDB(t *testing.T) {
	checkSyncDB := func(cloudMachines []joinMachine,
		databaseMachines []db.Machine, expected syncDBResult) syncDBResult {
		dbRes := syncDB(cloudMachines, databaseMachines)

		assert.Equal(t, expected.boot, dbRes.boot, "boot")
		assert.Equal(t, expected.stop, dbRes.stop, "stop")
		assert.Equal(t, expected.updateIPs, dbRes.updateIPs, "updateIPs")

		return dbRes
	}

	var noMachines []joinMachine
	dbNoSize := db.Machine{Provider: FakeAmazon, Region: testRegion}
	cmNoSize := joinMachine{provider: FakeAmazon, region: testRegion}
	dbLarge := db.Machine{Provider: FakeAmazon, Size: "m4.large", Region: testRegion}
	cmLarge := joinMachine{
		provider: FakeAmazon,
		region:   testRegion,
		Machine:  machine.Machine{Size: "m4.large"},
	}

	dbMaster := db.Machine{Provider: FakeAmazon, Role: db.Master}
	cmMasterList := joinMachine{provider: FakeAmazon, role: db.Master}
	cmMasterBoot := joinMachine{provider: FakeAmazon,
		Machine: machine.Machine{
			CloudCfgOpts: cloudcfg.Options{
				MinionOpts: cloudcfg.MinionOptions{
					Role: db.Master,
				},
			},
		},
	}
	dbWorker := db.Machine{Provider: FakeAmazon, Role: db.Worker}
	cmWorkerList := joinMachine{provider: FakeAmazon, role: db.Worker}
	cmWorkerBoot := joinMachine{provider: FakeAmazon,
		Machine: machine.Machine{
			CloudCfgOpts: cloudcfg.Options{
				MinionOpts: cloudcfg.MinionOptions{
					Role: db.Worker,
				},
			},
		},
	}

	cmNoIP := joinMachine{provider: FakeAmazon, Machine: machine.Machine{ID: "id"}}
	cmWithIP := joinMachine{
		provider: FakeAmazon,
		Machine:  machine.Machine{ID: "id", FloatingIP: "ip"},
	}
	dbNoIP := db.Machine{Provider: FakeAmazon, CloudID: "id"}
	dbWithIP := db.Machine{Provider: FakeAmazon, CloudID: "id", FloatingIP: "ip"}

	// Test boot with no size
	checkSyncDB(noMachines, []db.Machine{dbNoSize, dbNoSize}, syncDBResult{
		boot: []joinMachine{cmNoSize, cmNoSize},
	})

	// Test boot with size
	checkSyncDB(noMachines, []db.Machine{dbLarge, dbLarge}, syncDBResult{
		boot: []joinMachine{cmLarge, cmLarge},
	})

	// Test mixed boot
	checkSyncDB(noMachines, []db.Machine{dbNoSize, dbLarge}, syncDBResult{
		boot: []joinMachine{cmNoSize, cmLarge},
	})

	// Test partial boot
	checkSyncDB([]joinMachine{cmNoSize}, []db.Machine{dbNoSize, dbLarge},
		syncDBResult{
			boot: []joinMachine{cmLarge},
		},
	)

	// Test stop
	checkSyncDB([]joinMachine{cmNoSize, cmNoSize}, []db.Machine{}, syncDBResult{
		stop: []joinMachine{cmNoSize, cmNoSize},
	})

	// Test partial stop
	checkSyncDB([]joinMachine{cmNoSize, cmLarge}, []db.Machine{}, syncDBResult{
		stop: []joinMachine{cmNoSize, cmLarge},
	})

	// Test assign Floating IP
	checkSyncDB([]joinMachine{cmNoIP}, []db.Machine{dbWithIP}, syncDBResult{
		updateIPs: []joinMachine{cmWithIP},
	})

	// Test remove Floating IP
	checkSyncDB([]joinMachine{cmWithIP}, []db.Machine{dbNoIP}, syncDBResult{
		updateIPs: []joinMachine{cmNoIP},
	})

	// Test replace Floating IP
	cNewIP := joinMachine{
		provider: FakeAmazon,
		Machine:  machine.Machine{ID: "id", FloatingIP: "ip^"},
	}
	checkSyncDB([]joinMachine{cNewIP}, []db.Machine{dbWithIP}, syncDBResult{
		updateIPs: []joinMachine{cmWithIP},
	})

	// Test bad disk size
	checkSyncDB([]joinMachine{{Machine: machine.Machine{DiskSize: 3}}},
		[]db.Machine{{DiskSize: 4}},
		syncDBResult{
			stop: []joinMachine{{Machine: machine.Machine{DiskSize: 3}}},
			boot: []joinMachine{{Machine: machine.Machine{DiskSize: 4}}},
		})

	// Test different roles
	checkSyncDB([]joinMachine{cmWorkerList}, []db.Machine{dbMaster}, syncDBResult{
		boot: []joinMachine{cmMasterBoot},
		stop: []joinMachine{cmWorkerList},
	})

	checkSyncDB([]joinMachine{cmMasterList}, []db.Machine{dbWorker}, syncDBResult{
		boot: []joinMachine{cmWorkerBoot},
		stop: []joinMachine{cmMasterList},
	})

	// Test reserved instances.
	checkSyncDB([]joinMachine{{Machine: machine.Machine{Preemptible: true}}},
		[]db.Machine{{Preemptible: false}},
		syncDBResult{
			boot: []joinMachine{
				{Machine: machine.Machine{Preemptible: false}},
			},
			stop: []joinMachine{
				{Machine: machine.Machine{Preemptible: true}},
			},
		})

	// Test matching role as priority over PublicIP
	dbMaster.PublicIP = "worker"
	cmMasterList.PublicIP = "master"
	dbWorker.PublicIP = "master"
	cmWorkerList.PublicIP = "worker"

	checkSyncDB([]joinMachine{cmMasterList, cmWorkerList},
		[]db.Machine{dbMaster, dbWorker},
		syncDBResult{})

	// Test shuffling roles before CloudID is assigned
	dbw1 := db.Machine{Provider: FakeAmazon, Role: db.Worker, PublicIP: "w1"}
	dbw2 := db.Machine{Provider: FakeAmazon, Role: db.Worker, PublicIP: "w2"}
	dbw3 := db.Machine{Provider: FakeAmazon, Role: db.Worker, PublicIP: "w3"}

	mw1 := joinMachine{provider: FakeAmazon, role: db.Worker,
		Machine: machine.Machine{ID: "mw1", PublicIP: "w1"}}
	mw2 := joinMachine{provider: FakeAmazon, role: db.Worker,
		Machine: machine.Machine{ID: "mw2", PublicIP: "w2"}}
	mw3 := joinMachine{provider: FakeAmazon, role: db.Worker,
		Machine: machine.Machine{ID: "mw3", PublicIP: "w3"}}

	pair1 := join.Pair{L: dbw1, R: mw1}
	pair2 := join.Pair{L: dbw2, R: mw2}
	pair3 := join.Pair{L: dbw3, R: mw3}

	exp := []join.Pair{
		pair1,
		pair2,
		pair3,
	}

	pairs := checkSyncDB([]joinMachine{mw1, mw2, mw3},
		[]db.Machine{dbw1, dbw2, dbw3},
		syncDBResult{})

	assert.Equal(t, exp, pairs.pairs)

	// Test FloatingIP without role
	dbf1 := db.Machine{Provider: FakeAmazon, Role: db.Master, PublicIP: "master"}
	dbf2 := db.Machine{Provider: FakeAmazon, Role: db.Worker, PublicIP: "worker",
		FloatingIP: "float"}

	cmf1 := joinMachine{provider: FakeAmazon,
		Machine: machine.Machine{PublicIP: "worker", ID: "worker"}}
	cmf2 := joinMachine{provider: FakeAmazon,
		Machine: machine.Machine{PublicIP: "master", ID: "master"}}

	// No roles, CloudIDs not assigned, so nothing should happen
	checkSyncDB([]joinMachine{cmf1, cmf2},
		[]db.Machine{dbf1, dbf2},
		syncDBResult{})

	cmf1.role = db.Worker

	// One role assigned, so one CloudID to be assigned after
	checkSyncDB([]joinMachine{cmf1, cmf2},
		[]db.Machine{dbf1, dbf2},
		syncDBResult{})

	dbf2.CloudID = cmf1.ID
	cmf2.role = db.Master

	// Now that CloudID of machine with FloatingIP has been assigned,
	// FloatingIP should also be assigned
	checkSyncDB([]joinMachine{cmf1, cmf2},
		[]db.Machine{dbf1, dbf2},
		syncDBResult{
			updateIPs: []joinMachine{
				{
					provider: FakeAmazon,
					role:     db.Worker,
					Machine: machine.Machine{
						PublicIP:   "worker",
						ID:         "worker",
						FloatingIP: "float",
					},
				},
			},
		})

	// Test FloatingIP role shuffling
	dbm2 := db.Machine{Provider: FakeAmazon, Role: db.Master, PublicIP: "mIP"}
	dbm3 := db.Machine{Provider: FakeAmazon, Role: db.Worker, PublicIP: "wIP1",
		FloatingIP: "flip1"}
	dbm4 := db.Machine{Provider: FakeAmazon, Role: db.Worker, PublicIP: "wIP2",
		FloatingIP: "flip2"}

	m2 := joinMachine{provider: FakeAmazon,
		Machine: machine.Machine{PublicIP: "mIP", ID: "m2"}}
	m3 := joinMachine{provider: FakeAmazon,
		Machine: machine.Machine{PublicIP: "wIP1", ID: "m3"}}
	m4 := joinMachine{provider: FakeAmazon,
		Machine: machine.Machine{PublicIP: "wIP2", ID: "m4"}}

	m2.role = db.Worker
	m3.role = db.Master
	m4.role = db.Worker

	// CloudIDs not assigned to db machines yet, so shouldn't update anything.
	checkSyncDB([]joinMachine{m2, m3, m4},
		[]db.Machine{dbm2, dbm3, dbm4},
		syncDBResult{})

	dbm2.CloudID = m3.ID
	dbm3.CloudID = m2.ID
	dbm4.CloudID = m4.ID

	// CloudIDs are now assigned, so time to update floating IPs
	checkSyncDB([]joinMachine{m2, m3, m4},
		[]db.Machine{dbm2, dbm3, dbm4},
		syncDBResult{
			updateIPs: []joinMachine{
				{
					provider: FakeAmazon,
					role:     db.Worker,
					Machine: machine.Machine{
						PublicIP:   "mIP",
						ID:         "m2",
						FloatingIP: "flip1",
					},
				},
				{
					provider: FakeAmazon,
					role:     db.Worker,
					Machine: machine.Machine{
						PublicIP:   "wIP2",
						ID:         "m4",
						FloatingIP: "flip2",
					},
				},
			},
		})

}

func TestSync(t *testing.T) {
	type assertion struct {
		boot      []bootRequest
		stop      []string
		updateIPs []ipRequest
	}

	checkSync := func(clst *cluster, provider db.Provider, region string,
		expected assertion) {

		clst.runOnce()
		inst := launchLoc{provider, region}
		providerInst := clst.providers[inst].(*fakeProvider)

		if !emptySlices(expected.boot, providerInst.bootRequests) {
			assert.Equal(t, expected.boot, providerInst.bootRequests,
				"bootRequests")
		}

		if !emptySlices(expected.stop, providerInst.stopRequests) {
			assert.Equal(t, expected.stop, providerInst.stopRequests,
				"stopRequests")
		}

		if !emptySlices(expected.updateIPs, providerInst.updateIPs) {
			assert.Equal(t, expected.updateIPs, providerInst.updateIPs,
				"updateIPs")
		}

		providerInst.clearLogs()
	}

	amazonLargeBoot := bootRequest{size: "m4.large", cloudConfig: amazonCloudConfig}
	amazonXLargeBoot := bootRequest{size: "m4.xlarge",
		cloudConfig: amazonCloudConfig}
	vagrantLargeBoot := bootRequest{size: "vagrant.large",
		cloudConfig: vagrantCloudConfig}

	// Test initial boot
	clst := newTestCluster("ns")
	setNamespace(clst.conn, "ns")
	clst.conn.Txn(db.AllTables...).Run(func(view db.Database) error {
		m := view.InsertMachine()
		m.Role = db.Master
		m.Provider = FakeAmazon
		m.Region = testRegion
		m.Size = "m4.large"
		view.Commit(m)

		return nil
	})
	checkSync(clst, FakeAmazon, testRegion,
		assertion{boot: []bootRequest{amazonLargeBoot}})

	// Test adding a machine with the same provider
	clst.conn.Txn(db.AllTables...).Run(func(view db.Database) error {
		m := view.InsertMachine()
		m.Role = db.Master
		m.Provider = FakeAmazon
		m.Region = testRegion
		m.Size = "m4.xlarge"
		view.Commit(m)

		return nil
	})
	checkSync(clst, FakeAmazon, testRegion,
		assertion{boot: []bootRequest{amazonXLargeBoot}})

	// Test adding a machine with a different provider
	clst.conn.Txn(db.AllTables...).Run(func(view db.Database) error {
		m := view.InsertMachine()
		m.Role = db.Master
		m.Provider = FakeVagrant
		m.Region = testRegion
		m.Size = "vagrant.large"
		view.Commit(m)

		return nil
	})
	checkSync(clst, FakeVagrant, testRegion,
		assertion{boot: []bootRequest{vagrantLargeBoot}})

	// Test removing a machine
	var toRemove db.Machine
	clst.conn.Txn(db.AllTables...).Run(func(view db.Database) error {
		toRemove = view.SelectFromMachine(func(m db.Machine) bool {
			return m.Provider == FakeAmazon && m.Size == "m4.xlarge"
		})[0]
		view.Remove(toRemove)

		return nil
	})
	checkSync(clst, FakeAmazon, testRegion,
		assertion{stop: []string{toRemove.CloudID}})

	// Test booting a machine with floating IP - shouldn't update FloatingIP yet
	clst.conn.Txn(db.AllTables...).Run(func(view db.Database) error {
		m := view.InsertMachine()
		m.Role = db.Master
		m.Provider = FakeAmazon
		m.Size = "m4.large"
		m.Region = testRegion
		m.FloatingIP = "ip"
		view.Commit(m)

		return nil
	})
	checkSync(clst, FakeAmazon, testRegion, assertion{
		boot: []bootRequest{amazonLargeBoot},
	})

	// The bootRequest from the previous test is done now, and a CloudID has
	// been assigned, so we should also receive the ipRequest from before
	checkSync(clst, FakeAmazon, testRegion, assertion{
		updateIPs: []ipRequest{{
			size:        "m4.large",
			cloudConfig: amazonCloudConfig,
			ip:          "ip",
		}},
	})

	// Test assigning a floating IP to an existing machine
	clst.conn.Txn(db.AllTables...).Run(func(view db.Database) error {
		toAssign := view.SelectFromMachine(func(m db.Machine) bool {
			return m.Provider == FakeAmazon &&
				m.Size == "m4.large" &&
				m.FloatingIP == ""
		})[0]
		toAssign.FloatingIP = "another.ip"
		view.Commit(toAssign)

		return nil
	})
	checkSync(clst, FakeAmazon, testRegion, assertion{
		updateIPs: []ipRequest{
			{
				size:        "m4.large",
				cloudConfig: amazonCloudConfig,
				ip:          "another.ip",
			},
		},
	})

	// Test removing a floating IP
	clst.conn.Txn(db.AllTables...).Run(func(view db.Database) error {
		toUpdate := view.SelectFromMachine(func(m db.Machine) bool {
			return m.Provider == FakeAmazon &&
				m.Size == "m4.large" &&
				m.FloatingIP == "ip"
		})[0]
		toUpdate.FloatingIP = ""
		view.Commit(toUpdate)

		return nil
	})
	checkSync(clst, FakeAmazon, testRegion, assertion{
		updateIPs: []ipRequest{{
			size:        "m4.large",
			cloudConfig: amazonCloudConfig,
			ip:          "",
		}},
	})

	// Test removing and adding a machine
	clst.conn.Txn(db.AllTables...).Run(func(view db.Database) error {
		toRemove = view.SelectFromMachine(func(m db.Machine) bool {
			return m.Provider == FakeAmazon && m.Size == "m4.large"
		})[0]
		view.Remove(toRemove)

		m := view.InsertMachine()
		m.Role = db.Worker
		m.Provider = FakeAmazon
		m.Size = "m4.xlarge"
		m.Region = testRegion
		view.Commit(m)

		return nil
	})
	checkSync(clst, FakeAmazon, testRegion, assertion{
		boot: []bootRequest{amazonXLargeBoot},
		stop: []string{toRemove.CloudID},
	})

	// Test adding machine with different role
	clst.conn.Txn(db.AllTables...).Run(func(view db.Database) error {
		m := view.InsertMachine()
		m.Role = db.Master
		m.Provider = FakeAmazon
		m.Size = "m4.xlarge"
		m.Region = testRegion
		view.Commit(m)

		return nil
	})

	checkSync(clst, FakeAmazon, testRegion, assertion{
		boot: []bootRequest{amazonXLargeBoot},
	})

	clst.conn.Txn(db.AllTables...).Run(func(view db.Database) error {
		toRemove = view.SelectFromMachine(func(m db.Machine) bool {
			return m.Role == db.Master && m.Size == "m4.xlarge" &&
				m.Provider == FakeAmazon
		})[0]
		view.Remove(toRemove)
		m := view.InsertMachine()
		m.Role = db.Worker
		m.Provider = FakeAmazon
		m.Size = "m4.xlarge"
		m.Region = testRegion
		view.Commit(m)

		return nil
	})

	checkSync(clst, FakeAmazon, testRegion, assertion{
		boot: []bootRequest{amazonXLargeBoot},
		stop: []string{toRemove.CloudID},
	})
}

func TestACLs(t *testing.T) {
	myIP = func() (string, error) {
		return "5.6.7.8", nil
	}

	clst := newTestCluster("ns")
	clst.syncACLs([]string{"admin"},
		[]db.PortRange{
			{
				MinPort: 80,
				MaxPort: 80,
			},
		},
		[]db.Machine{
			{
				Provider: FakeAmazon,
				PublicIP: "8.8.8.8",
				Region:   testRegion,
			},
			{},
		},
	)

	exp := []acl.ACL{
		{
			CidrIP:  "admin",
			MinPort: 1,
			MaxPort: 65535,
		},
		{
			CidrIP:  "5.6.7.8/32",
			MinPort: 1,
			MaxPort: 65535,
		},
		{
			CidrIP:  "0.0.0.0/0",
			MinPort: 80,
			MaxPort: 80,
		},
		{
			CidrIP:  "8.8.8.8/32",
			MinPort: 1,
			MaxPort: 65535,
		},
	}
	inst := launchLoc{FakeAmazon, testRegion}
	actual := clst.providers[inst].(*fakeProvider).aclRequests
	assert.Equal(t, exp, actual)
}

func TestUpdateCluster(t *testing.T) {
	conn := db.New()

	clst := updateCluster(conn, nil)
	assert.Nil(t, clst)

	setNamespace(conn, "ns1")
	clst = updateCluster(conn, clst)
	assert.NotNil(t, clst)
	assert.Equal(t, "ns1", clst.namespace)

	inst := launchLoc{FakeAmazon, testRegion}
	amzn := clst.providers[inst].(*fakeProvider)
	assert.Empty(t, amzn.bootRequests)
	assert.Empty(t, amzn.stopRequests)
	assert.Equal(t, "ns1", amzn.namespace)

	conn.Txn(db.AllTables...).Run(func(view db.Database) error {
		m := view.InsertMachine()
		m.Provider = FakeAmazon
		m.Size = "size1"
		m.Region = testRegion
		view.Commit(m)
		return nil
	})

	oldClst := clst
	oldAmzn := amzn

	clst = updateCluster(conn, clst)
	assert.NotNil(t, clst)

	// Pointers shouldn't have changed
	amzn = clst.providers[inst].(*fakeProvider)
	assert.True(t, oldClst == clst)
	assert.True(t, oldAmzn == amzn)

	assert.Empty(t, amzn.stopRequests)
	assert.Equal(t, []bootRequest{{
		size:        "size1",
		cloudConfig: amazonCloudConfig,
	}}, amzn.bootRequests)
	assert.Equal(t, "ns1", amzn.namespace)
	amzn.clearLogs()

	conn.Txn(db.AllTables...).Run(func(view db.Database) error {
		dbms := view.SelectFromMachine(nil)
		dbms[0].Size = "size2"
		view.Commit(dbms[0])
		return nil
	})

	oldClst = clst
	oldAmzn = amzn
	setNamespace(conn, "ns2")
	clst = updateCluster(conn, clst)
	assert.NotNil(t, clst)

	// Pointers should have changed
	amzn = clst.providers[inst].(*fakeProvider)
	assert.True(t, oldClst != clst)
	assert.True(t, oldAmzn != amzn)

	assert.Equal(t, "ns1", oldAmzn.namespace)
	assert.Empty(t, oldAmzn.bootRequests)
	assert.Empty(t, oldAmzn.stopRequests)

	assert.Equal(t, "ns2", amzn.namespace)
	assert.Equal(t, []bootRequest{{
		size:        "size2",
		cloudConfig: amazonCloudConfig,
	}}, amzn.bootRequests)
	assert.Empty(t, amzn.stopRequests)
}

func TestMultiRegionDeploy(t *testing.T) {
	clst := newTestCluster("ns")
	clst.conn.Txn(db.MachineTable,
		db.ClusterTable).Run(func(view db.Database) error {

		for _, p := range allProviders {
			for _, r := range validRegions(p) {
				m := view.InsertMachine()
				m.Provider = p
				m.Region = r
				m.Size = "size1"
				view.Commit(m)
			}
		}

		c := view.InsertCluster()
		c.Namespace = "ns"
		view.Commit(c)
		return nil
	})

	for i := 0; i < 2; i++ {
		clst.runOnce()
		cloudMachines, err := clst.get()
		assert.NoError(t, err)
		dbMachines := clst.conn.SelectFromMachine(nil)
		joinResult := syncDB(cloudMachines, dbMachines)

		// All machines should be booted
		assert.Empty(t, joinResult.boot)
		assert.Empty(t, joinResult.stop)
		assert.Len(t, joinResult.pairs, len(dbMachines))
	}

	clst.conn.Txn(db.MachineTable).Run(func(view db.Database) error {
		m := view.SelectFromMachine(func(m db.Machine) bool {
			return m.Provider == FakeAmazon &&
				m.Region == validRegions(FakeAmazon)[0]
		})

		assert.Len(t, m, 1)
		view.Remove(m[0])
		return nil
	})

	clst.runOnce()
	machinesRemaining, err := clst.get()
	assert.NoError(t, err)

	assert.NotContains(t, machinesRemaining, joinMachine{
		provider: FakeAmazon,
		region:   validRegions(FakeAmazon)[0],
		Machine:  machine.Machine{Size: "size1"},
	})
	cloudMachines, err := clst.get()
	assert.NoError(t, err)
	dbMachines := clst.conn.SelectFromMachine(nil)
	joinResult := syncDB(cloudMachines, dbMachines)

	assert.Empty(t, joinResult.boot)
	assert.Empty(t, joinResult.stop)
	assert.Len(t, joinResult.pairs, len(dbMachines))
}

func TestGetError(t *testing.T) {
	t.Parallel()

	_, err := cluster{
		providers: map[launchLoc]provider{
			{db.Amazon, "us-west-1"}: &fakeProvider{
				listError: errors.New("err"),
			},
		},
	}.get()
	assert.EqualError(t, err, "list Amazon-us-west-1: err")

	_, err = cluster{
		providers: map[launchLoc]provider{
			{provider: db.Vagrant}: &fakeProvider{
				listError: errors.New("err"),
			},
		},
	}.get()
	assert.EqualError(t, err, "list Vagrant: err")
}

func setNamespace(conn db.Conn, ns string) {
	conn.Txn(db.AllTables...).Run(func(view db.Database) error {
		clst, err := view.GetCluster()
		if err != nil {
			clst = view.InsertCluster()
		}

		clst.Namespace = ns
		view.Commit(clst)
		return nil
	})
}

func mock() {
	var instantiatedProviders []fakeProvider
	newProvider = func(p db.Provider, namespace, region string) (provider, error) {
		ret := fakeProvider{
			namespace: namespace,
			machines:  make(map[string]machine.Machine),
			roles:     make(map[string]db.Role),
		}
		ret.clearLogs()

		switch p {
		case FakeAmazon:
			ret.cloudConfig = amazonCloudConfig
		case FakeVagrant:
			ret.cloudConfig = vagrantCloudConfig
		default:
			panic("Unreached")
		}

		instantiatedProviders = append(instantiatedProviders, ret)
		return &ret, nil
	}

	validRegions = fakeValidRegions
	allProviders = []db.Provider{FakeAmazon, FakeVagrant}
	getMachineRole = func(ip string) db.Role {
		for _, prvdr := range instantiatedProviders {
			if role, ok := prvdr.roles[ip]; ok {
				return role
			}
		}
		return db.None
	}
}

func emptySlices(slice1 interface{}, slice2 interface{}) bool {
	return reflect.ValueOf(slice1).Len() == 0 && reflect.ValueOf(slice2).Len() == 0
}
