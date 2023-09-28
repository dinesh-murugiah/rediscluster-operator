package redisutil

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"time"

	utils "github.com/dinesh-murugiah/rediscluster-operator/utils/commonutils"
	"github.com/go-logr/logr"
	"github.com/redis/go-redis/v9"
)

const (
	// defaultHashMaxSlots higher value of slot
	// as slots start at 0, total number of slots is defaultHashMaxSlots+1
	DefaultHashMaxSlots = 16383

	// ResetHard HARD mode for RESET command
	ResetHard = "HARD"
	// ResetSoft SOFT mode for RESET command
	ResetSoft = "SOFT"
)

const (
	clusterKnownNodesREString = "cluster_known_nodes:([0-9]+)"
)

var (
	clusterKnownNodesRE = regexp.MustCompile(clusterKnownNodesREString)
)

// IAdmin redis cluster admin interface
type IAdmin interface {
	// Connections returns the connection map of all clients
	Connections() IAdminConnections
	// Close the admin connections
	Close(ctx context.Context)
	// GetClusterInfos get node infos for all nodes
	GetClusterInfos(ctx context.Context) (*ClusterInfos, error)
	// ClusterManagerNodeIsEmpty Checks whether the node is empty. Node is considered not-empty if it has
	// some key or if it already knows other nodes
	ClusterManagerNodeIsEmpty(ctx context.Context) (bool, error)
	// SetConfigEpoch Assign a different config epoch to each node
	SetConfigEpoch(ctx context.Context) error
	// SetConfigIfNeed set redis config
	SetConfigIfNeed(ctx context.Context, newConfig map[string]string) error
	// GetAllConfig get redis config by CONFIG GET *
	GetAllConfig(ctx context.Context, c *redis.Client, addr string) (map[string]string, error)
	// AttachNodeToCluster command use to connect a Node to the cluster
	// the connection will be done on a random node part of the connection pool
	AttachNodeToCluster(ctx context.Context, addr string) error
	// AttachSlaveToMaster attach a slave to a master node
	AttachSlaveToMaster(ctx context.Context, slave *Node, masterID string) error
	// DetachSlave dettach a slave to its master
	DetachSlave(ctx context.Context, slave *Node) error
	// ForgetNode execute the Redis command to force the cluster to forgot the the Node
	ForgetNode(ctx context.Context, id string) error
	// SetSlots exec the redis command to set slots in a pipeline, provide
	// and empty nodeID if the set slots commands doesn't take a nodeID in parameter
	SetSlots(ctx context.Context, addr string, action string, slots []Slot, nodeID string) error
	// AddSlots exec the redis command to add slots in a pipeline
	AddSlots(ctx context.Context, addr string, slots []Slot) error
	// SetSlot use to set SETSLOT command on a slot
	SetSlot(ctx context.Context, addr string, action string, slot Slot, nodeID string) error
	// MigrateKeys from addr to destination node. returns number of slot migrated. If replace is true, replace key on busy error
	MigrateKeys(ctx context.Context, addr string, dest *Node, slots []Slot, batch, timeout int, replace bool) (int, error)
	// MigrateKeys use to migrate keys from slot to other slot. if replace is true, replace key on busy error
	// timeout is in milliseconds
	MigrateKeysInSlot(ctx context.Context, addr string, dest *Node, slot Slot, batch int, timeout int, replace bool) (int, error)
	// FlushAndReset reset the cluster configuration of the node, the node is flushed in the same pipe to ensure reset works
	FlushAndReset(ctx context.Context, addr string, mode string) error
	// GetHashMaxSlot get the max slot value
	GetHashMaxSlot() Slot
	// ResetPassword reset redis node masterauth and requirepass.
	ResetPassword(ctx context.Context, newPassword string) error
}

// AdminOptions optional options for redis admin
type AdminOptions struct {
	ConnectionTimeout  time.Duration
	ClientName         string
	RenameCommandsFile string
	Password           string
}

// Admin wraps redis cluster admin logic
type Admin struct {
	hashMaxSlots Slot
	cnx          IAdminConnections
	log          logr.Logger
}

// NewAdmin returns new AdminInterface instance
// at the same time it connects to all Redis Nodes thanks to the addrs list
func NewAdmin(addrs []string, options *AdminOptions, log logr.Logger, ctx context.Context) IAdmin {
	a := &Admin{
		hashMaxSlots: DefaultHashMaxSlots,
		log:          log.WithName("redis_util"),
	}

	// perform initial connections
	a.cnx = NewAdminConnections(addrs, options, log, ctx)

	return a
}

// Connections returns the connection map of all clients
func (a *Admin) Connections() IAdminConnections {
	return a.cnx
}

// Close used to close all possible resources instanciate by the Admin
func (a *Admin) Close(ctx context.Context) {
	a.Connections().Reset(ctx)
}

// GetClusterInfos return the Nodes infos for all nodes
func (a *Admin) GetClusterInfos(ctx context.Context) (*ClusterInfos, error) {
	infos := NewClusterInfos()
	clusterErr := NewClusterInfosError()

	for addr, c := range a.Connections().GetAll() {
		nodeinfos, err := a.getInfos(ctx, c, addr)
		if err != nil {
			a.log.WithValues("err", err).Info("get redis info failed")
			infos.Status = ClusterInfosPartial
			clusterErr.partial = true
			clusterErr.errs[addr] = err
			continue
		}
		if nodeinfos.Node != nil && nodeinfos.Node.IPPort() == addr {
			infos.Infos[addr] = nodeinfos
		} else {
			a.log.Info("bad node info retrieved from", "addr", addr)
		}
	}

	if len(clusterErr.errs) == 0 {
		clusterErr.inconsistent = !infos.ComputeStatus(a.log)
	}
	if infos.Status == ClusterInfosConsistent {
		return infos, nil
	}
	return infos, clusterErr
}

func (a *Admin) getInfos(ctx context.Context, c *redis.Client, addr string) (*NodeInfos, error) {
	resp := c.Do(ctx, "CLUSTER", "NODES")
	if err := a.Connections().ValidateResp(ctx, resp, addr, "unable to retrieve node info"); err != nil {
		return nil, err
	}
	//var err error
	raw, _ := resp.Result()
	raw_str := fmt.Sprintf("%v", raw)
	nodeInfos := DecodeNodeInfos(&raw_str, addr, a.log)
	return nodeInfos, nil
}

// ClusterManagerNodeIsEmpty Checks whether the node is empty. Node is considered not-empty if it has
// some key or if it already knows other nodes
func (a *Admin) ClusterManagerNodeIsEmpty(ctx context.Context) (bool, error) {
	for addr, c := range a.Connections().GetAll() {
		knowNodes, err := a.clusterKnowNodes(ctx, c, addr)
		if err != nil {
			return false, err
		}
		if knowNodes != 1 {
			return false, nil
		}
	}
	return true, nil
}

func (a *Admin) clusterKnowNodes(ctx context.Context, client *redis.Client, addr string) (int, error) {
	resp := client.Do(ctx, "CLUSTER", "INFO")
	if err := a.Connections().ValidateResp(ctx, resp, addr, "unable to retrieve cluster info"); err != nil {
		return 0, err
	}
	raw := resp.String()

	match := clusterKnownNodesRE.FindStringSubmatch(raw)
	if len(match) == 0 {
		return 0, fmt.Errorf("cluster_known_nodes regex not found")
	}
	return strconv.Atoi(match[1])
}

// AttachSlaveToMaster attach a slave to a master node
func (a *Admin) AttachSlaveToMaster(ctx context.Context, slave *Node, masterID string) error {
	c, err := a.Connections().Get(ctx, slave.IPPort())
	if err != nil {
		return err
	}

	resp := c.Do(ctx, "CLUSTER", "REPLICATE", masterID)
	if err := a.Connections().ValidateResp(ctx, resp, slave.IPPort(), "unable to run command REPLICATE"); err != nil {
		return err
	}
	if resp.Err() != nil {
		return err
	}
	slave.SetReferentMaster(masterID)
	slave.SetRole(RedisSlaveRole)

	return nil
}

// AddSlots use to ADDSLOT commands on several slots
func (a *Admin) AddSlots(ctx context.Context, addr string, slots []Slot) error {
	if len(slots) == 0 {
		return nil
	}
	c, err := a.Connections().Get(ctx, addr)
	if err != nil {
		return err
	}
	firstSlot := slots[0].ToInt()
	lastSlot := slots[len(slots)-1].ToInt()
	resp := c.ClusterAddSlotsRange(ctx, firstSlot, lastSlot)
	if resp.Err() != nil {
		return resp.Err()
	}
	return err
	// TODO - add validateresp to handle  *redis.StatusCmd, currently it handles only *redis.Cmd
	//return a.Connections().ValidateResp(ctx, resp, addr, "unable to run CLUSTER ADDSLOTS")
}

// SetSlots use to set SETSLOT command on several slots
func (a *Admin) SetSlots(ctx context.Context, addr, action string, slots []Slot, nodeID string) error {
	if len(slots) == 0 {
		return nil
	}
	c, err := a.Connections().Get(ctx, addr)

	if err != nil {
		return err
	}
	pipeClient := c.Pipeline()

	for _, slot := range slots {
		slotint := slot.ToInt()
		args := []interface{}{"CLUSTER", "SETSLOT", slotint, action}
		if nodeID != "" {
			args = append(args, nodeID)
		}
		pipeClient.Do(ctx, args...)
	}

	out, err := pipeClient.Exec(ctx)

	if err != nil {
		return err
	}

	if !a.Connections().ValidatePipeResp(ctx, out, addr, err, "Cannot SETSLOTS") {
		return fmt.Errorf("Error occured during CLUSTER SETSLOT %s", action)
	}
	// TODO : check need to clear pipeline
	//c.PipeClear()

	return nil
}

// SetSlot use to set SETSLOT command on a slot
func (a *Admin) SetSlot(ctx context.Context, addr, action string, slot Slot, nodeID string) error {
	c, err := a.Connections().Get(ctx, addr)
	if err != nil {
		return err
	}
	pipeClient := c.Pipeline()
	slotint := slot.ToInt()
	args := []interface{}{"CLUSTER", "SETSLOT", slotint, action}

	if nodeID != "" {
		args = append(args, nodeID)
	}
	pipeClient.Do(ctx, args...)
	out, err := pipeClient.Exec(ctx)
	// TODO: Handle response

	if !a.Connections().ValidatePipeResp(ctx, out, addr, err, "Cannot SETSLOT") {
		return fmt.Errorf("Error occured during CLUSTER SETSLOT %s", action)
	}
	// TODO : check need to clear pipeline
	//c.PipeClear()

	return nil
}

func (a *Admin) SetConfigEpoch(ctx context.Context) error {
	configEpoch := 1
	for addr, c := range a.Connections().GetAll() {
		resp := c.Do(ctx, "CLUSTER", "SET-CONFIG-EPOCH", configEpoch)
		if err := a.Connections().ValidateResp(ctx, resp, addr, "unable to run command SET-CONFIG-EPOCH"); err != nil {
			return err
		}
		// dummy print
		//fmt.Println(addr)
		if resp.Err() != nil {
			return resp.Err()
		}
		configEpoch++
	}
	return nil
}

// AttachNodeToCluster command use to connect a Node to the cluster
func (a *Admin) AttachNodeToCluster(ctx context.Context, addr string) error {
	ip, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}

	all := a.Connections().GetAll()
	if len(all) == 0 {
		return fmt.Errorf("no connection for other redis-node found")
	}
	for cAddr, c := range a.Connections().GetAll() {
		if cAddr == addr {
			continue
		}
		a.log.V(3).Info("CLUSTER MEET", "from addr", cAddr, "to", addr)
		resp := c.Do(ctx, "CLUSTER", "MEET", ip, port)
		if err = a.Connections().ValidateResp(ctx, resp, addr, "cannot attach node to cluster"); err != nil {
			return err
		}
		if resp.Err() != nil {
			return resp.Err()
		}
	}
	a.Connections().Add(ctx, addr)
	a.log.Info(fmt.Sprintf("node %s attached properly", addr))
	return nil
}

// GetAllConfig get redis config by CONFIG GET *
func (a *Admin) GetAllConfig(ctx context.Context, c *redis.Client, addr string) (map[string]string, error) {
	resp := c.Do(ctx, "CONFIG", "GET", "*")
	if err := a.Connections().ValidateResp(ctx, resp, addr, "unable to retrieve config"); err != nil {
		return nil, err
	}
	if resp.Err() != nil {
		return nil, resp.Err()
	}

	res, err := resp.Result()

	originalMap := res.(map[interface{}]interface{})
	convertedMap := make(map[string]string)

	for key, value := range originalMap {
		// Check if the key and value are of the expected types
		keyStr, keyIsString := key.(string)
		valueStr, valueIsString := value.(string)

		if keyIsString && valueIsString {
			convertedMap[keyStr] = valueStr
		}
	}
	return convertedMap, err
}

var parseConfigMap = map[string]int8{
	"maxmemory":                  0,
	"proto-max-bulk-len":         0,
	"client-query-buffer-limit":  0,
	"repl-backlog-size":          0,
	"auto-aof-rewrite-min-size":  0,
	"active-defrag-ignore-bytes": 0,
	"hash-max-ziplist-entries":   0,
	"hash-max-ziplist-value":     0,
	"stream-node-max-bytes":      0,
	"set-max-intset-entries":     0,
	"zset-max-ziplist-entries":   0,
	"zset-max-ziplist-value":     0,
	"hll-sparse-max-bytes":       0,
	// TODO parse client-output-buffer-limit
	//"client-output-buffer-limit": 0,
}

// SetConfigIfNeed set redis config
func (a *Admin) SetConfigIfNeed(ctx context.Context, newConfig map[string]string) error {
	for addr, c := range a.Connections().GetAll() {
		oldConfig, err := a.GetAllConfig(ctx, c, addr)
		if err != nil {
			return err
		}

		for key, value := range newConfig {
			var err error
			if _, ok := parseConfigMap[key]; ok {
				value, err = utils.ParseRedisMemConf(value)
				if err != nil {
					a.log.Error(err, "redis config format err", "key", key, "value", value)
					continue
				}
			}
			if value != oldConfig[key] {
				a.log.V(3).Info("CONFIG SET", key, value)
				resp := c.Do(ctx, "CONFIG", "SET", key, value)
				if err := a.Connections().ValidateResp(ctx, resp, addr, "unable to retrieve config"); err != nil {
					return err
				}
				if resp.Err() != nil {
					return resp.Err()
				}
			}
		}
	}
	return nil
}

// GetHashMaxSlot get the max slot value
func (a *Admin) GetHashMaxSlot() Slot {
	return a.hashMaxSlots
}

// MigrateKeys use to migrate keys from slots to other slots. if replace is true, replace key on busy error
// timeout is in milliseconds
func (a *Admin) MigrateKeys(ctx context.Context, addr string, dest *Node, slots []Slot, batch int, timeout int, replace bool) (int, error) {
	if len(slots) == 0 {
		return 0, nil
	}
	keyCount := 0
	c, err := a.Connections().Get(ctx, addr)
	if err != nil {
		return keyCount, err
	}
	timeoutStr := strconv.Itoa(timeout)
	batchStr := strconv.Itoa(batch)

	for _, slot := range slots {
		for {
			slotint := slot.ToInt()
			resp := c.Do(ctx, "CLUSTER", "GETKEYSINSLOT", slotint, batchStr)
			if resp.Err() != nil {
				return 0, resp.Err()
			}
			if err := a.Connections().ValidateResp(ctx, resp, addr, "Unable to run command GETKEYSINSLOT"); err != nil {
				return keyCount, err
			}
			res, err := resp.Result()
			if err != nil {
				return keyCount, err
			}

			//TODO:  better way to convert this to list
			keySlice, ok := res.([]interface{})
			if !ok {
				return keyCount, fmt.Errorf("unexpected type for res: %T", res)
			}

			keys := make([]string, len(keySlice))

			for i := 0; i < len(keySlice); i++ {
				key, ok := keySlice[i].(string)
				if !ok {
					return keyCount, fmt.Errorf("unexpected type for key at index %d: %T", i, keySlice[i])
				}
				keys[i] = key
			}

			keyCount += len(keys)
			if len(keys) == 0 {
				break
			}

			args := a.migrateCmdArgs(dest, timeoutStr, replace, keys)
			resp = c.Do(ctx, "MIGRATE", args)
			if resp.Err() != nil {
				return keyCount, err
			}
		}
	}

	return keyCount, nil
}

// MigrateKeys use to migrate keys from slot to other slot. if replace is true, replace key on busy error
// timeout is in milliseconds
func (a *Admin) MigrateKeysInSlot(ctx context.Context, addr string, dest *Node, slot Slot, batch int, timeout int, replace bool) (int, error) {
	keyCount := 0
	c, err := a.Connections().Get(ctx, addr)
	if err != nil {
		return keyCount, err
	}
	timeoutStr := strconv.Itoa(timeout)
	batchStr := strconv.Itoa(batch)

	slotint := slot.ToInt()

	for {
		resp := c.Do(ctx, "CLUSTER", "GETKEYSINSLOT", slotint, batchStr)
		if resp.Err() != nil {
			return 0, resp.Err()
		}
		if err := a.Connections().ValidateResp(ctx, resp, addr, "Unable to run command GETKEYSINSLOT"); err != nil {
			return keyCount, err
		}
		res, err := resp.Result()
		if err != nil {
			return keyCount, err
		}

		// TODO: Better way to convert to list
		keySlice, ok := res.([]interface{})
		if !ok {
			return keyCount, fmt.Errorf("unexpected type for res: %T", res)
		}

		keys := make([]string, len(keySlice))

		for i := 0; i < len(keySlice); i++ {
			key, ok := keySlice[i].(string)
			if !ok {
				return keyCount, fmt.Errorf("unexpected type for key at index %d: %T", i, keySlice[i])
			}
			keys[i] = key
		}

		keyCount += len(keys)
		if len(keys) == 0 {
			break
		}

		args := a.migrateCmdArgs(dest, timeoutStr, replace, keys)

		resp = c.Do(ctx, "MIGRATE", args)
		if err := a.Connections().ValidateResp(ctx, resp, addr, "Unable to run command MIGRATE"); err != nil {
			return keyCount, err
		}
		if resp.Err() != nil {
			return 0, resp.Err()
		}

	}

	return keyCount, nil
}

func (a *Admin) migrateCmdArgs(dest *Node, timeoutStr string, replace bool, keys []string) []string {
	args := []string{dest.IP, dest.Port, "", "0", timeoutStr}
	if password, ok := a.Connections().GetAUTH(); ok {
		args = append(args, "AUTH", password)
	}
	if replace {
		args = append(args, "REPLACE", "KEYS")
	} else {
		args = append(args, "KEYS")
	}
	args = append(args, keys...)
	return args
}

// ForgetNode used to force other redis cluster node to forget a specific node
func (a *Admin) ForgetNode(ctx context.Context, id string) error {
	infos, _ := a.GetClusterInfos(ctx)
	for nodeAddr, nodeinfos := range infos.Infos {
		if nodeinfos.Node.ID == id {
			continue
		}

		if IsSlave(nodeinfos.Node) && nodeinfos.Node.MasterReferent == id {
			if err := a.DetachSlave(ctx, nodeinfos.Node); err != nil {
				a.log.Error(err, "DetachSlave", "node", nodeAddr)
			}
			a.log.Info(fmt.Sprintf("detach slave id: %s of master: %s", nodeinfos.Node.ID, id))
		}

		c, err := a.Connections().Get(ctx, nodeAddr)
		if err != nil {
			a.log.Error(err, fmt.Sprintf("cannot force a forget on node %s, for node %s", nodeAddr, id))
			continue
		}

		a.log.Info("CLUSTER FORGET", "id", id, "from", nodeAddr)
		resp := c.Do(ctx, "CLUSTER", "FORGET", id)
		a.Connections().ValidateResp(ctx, resp, nodeAddr, "Unable to execute FORGET command")
		if resp.Err() != nil {
			return resp.Err()
		}
	}

	a.log.Info("Forget node done", "node", id)
	return nil
}

// DetachSlave use to detach a slave to a master
func (a *Admin) DetachSlave(ctx context.Context, slave *Node) error {
	c, err := a.Connections().Get(ctx, slave.IPPort())
	if err != nil {
		a.log.Error(err, fmt.Sprintf("unable to get the connection for slave ID:%s, addr:%s", slave.ID, slave.IPPort()))
		return err
	}

	resp := c.Do(ctx, "CLUSTER", "RESET", "SOFT")
	if err = a.Connections().ValidateResp(ctx, resp, slave.IPPort(), "cannot attach node to cluster"); err != nil {
		return err
	}
	if resp.Err() != nil {
		return resp.Err()
	}

	if err = a.AttachNodeToCluster(ctx, slave.IPPort()); err != nil {
		a.log.Error(err, fmt.Sprintf("[DetachSlave] unable to AttachNodeToCluster the Slave id: %s addr:%s", slave.ID, slave.IPPort()))
		return err
	}

	slave.SetReferentMaster("")
	slave.SetRole(RedisMasterRole)

	return nil
}

// FlushAndReset flush the cluster and reset the cluster configuration of the node. Commands are piped, to ensure no items arrived between flush and reset
func (a *Admin) FlushAndReset(ctx context.Context, addr string, mode string) error {
	c, err := a.Connections().Get(ctx, addr)
	if err != nil {
		return err
	}
	pipeClient := c.Pipeline()
	pipeClient.Do(ctx, "FLUSHALL")
	pipeClient.Do(ctx, "CLUSTER", "RESET", mode)

	out, err := pipeClient.Exec(ctx)

	if !a.Connections().ValidatePipeResp(ctx, out, addr, err, "Cannot reset node") {
		return fmt.Errorf("cannot reset node %s", addr)
	}

	return nil
}

// ResetPassword reset redis node masterauth and requirepass.
func (a *Admin) ResetPassword(ctx context.Context, newPassword string) error {
	all := a.Connections().GetAll()
	if len(all) == 0 {
		return fmt.Errorf("no connection for other redis-node found")
	}
	for addr, c := range a.Connections().GetAll() {
		a.log.Info("reset password", "addr", addr)
		resp := c.Do(ctx, "CONFIG", "SET", "masterauth", newPassword)
		if err := a.Connections().ValidateResp(ctx, resp, addr, "cannot set new masterauth"); err != nil {
			return err
		}
		if resp.Err() != nil {
			return resp.Err()
		}
		resp = c.Do(ctx, "CONFIG", "SET", "requirepass", newPassword)
		if err := a.Connections().ValidateResp(ctx, resp, addr, "cannot set new requirepass"); err != nil {
			return err
		}
		if resp.Err() != nil {
			return resp.Err()
		}
	}
	return nil
}
