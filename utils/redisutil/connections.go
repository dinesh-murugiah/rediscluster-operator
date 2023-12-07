package redisutil

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"os"
	"time"

	utils "github.com/dinesh-murugiah/rediscluster-operator/utils/commonutils"
	"github.com/go-logr/logr"
	redis "github.com/redis/go-redis/v9"
)

const (
	defaultClientTimeout = 2 * time.Second
	defaultClientName    = ""

	// ErrNotFound cannot find a node to connect to
	ErrNotFound = "unable to find a node to connect"
)

// IAdminConnections interface representing the map of admin connections to redis cluster nodes
type IAdminConnections interface {
	// Add connect to the given address and
	// register the client connection to the pool
	Add(ctx context.Context, addr string) error
	// Remove disconnect and remove the client connection from the map
	Remove(ctx context.Context, addr string)
	// Get returns a client connection for the given address,
	// connects if the connection is not in the map yet
	Get(ctx context.Context, addr string) (*redis.Client, error)
	// GetRandom returns a client connection to a random node of the client map
	GetRandom() (*redis.Client, error)
	// GetDifferentFrom returns a random client connection different from given address
	GetDifferentFrom(addr string) (*redis.Client, error)
	// GetAll returns a map of all clients per address
	GetAll() map[string]*redis.Client
	//GetSelected returns a map of clients based on the input addresses
	GetSelected(addrs []string) map[string]*redis.Client
	// Reconnect force a reconnection on the given address
	// if the adress is not part of the map, act like Add
	Reconnect(ctx context.Context, addr string) error
	// AddAll connect to the given list of addresses and
	// register them in the map
	// fail silently
	AddAll(ctx context.Context, addrs []string)
	// ReplaceAll clear the map and re-populate it with new connections
	// fail silently
	ReplaceAll(ctx context.Context, addrs []string)
	// ValidateResp check the redis resp, eventually reconnect on connection error
	// in case of error, customize the error, log it and return it
	ValidateResp(ctx context.Context, resp *redis.Cmd, addr, errMessage string) error
	// ValidatePipeResp wait for all answers in the pipe and validate the response
	// in case of network issue clear the pipe and return
	// in case of error return false
	ValidatePipeResp(ctx context.Context, resp []redis.Cmder, addr string, err error, errMessage string) bool
	// Reset close all connections and clear the connection map
	Reset(ctx context.Context)
	// GetAUTH return password and true if connection password is set, else return false.
	GetAUTH() (string, bool)
}

// AdminConnections connection map for redis cluster
// currently the admin connection is not threadSafe since it is only use in the Events thread.
type AdminConnections struct {
	clients           map[string]*redis.Client
	connectionTimeout time.Duration
	commandsMapping   map[string]string
	clientName        string
	password          string
	log               logr.Logger
}

func init() {
	rand.Seed(time.Now().UnixNano())
}

// NewAdminConnections returns and instance of AdminConnectionsInterface
func NewAdminConnections(addrs []string, options *AdminOptions, log logr.Logger, ctx context.Context) IAdminConnections {
	cnx := &AdminConnections{
		clients:           make(map[string]*redis.Client),
		connectionTimeout: defaultClientTimeout,
		commandsMapping:   make(map[string]string),
		clientName:        defaultClientName,
		log:               log,
	}
	if options != nil {
		if options.ConnectionTimeout != 0 {
			cnx.connectionTimeout = options.ConnectionTimeout
		}
		if _, err := os.Stat(options.RenameCommandsFile); err == nil {
			cnx.commandsMapping = utils.BuildCommandReplaceMapping(options.RenameCommandsFile, cnx.log)
		}
		cnx.clientName = options.ClientName
		cnx.password = options.Password
	}
	cnx.AddAll(ctx, addrs)
	return cnx
}

// GetAUTH return password and true if connection password is set, else return false.
func (cnx *AdminConnections) GetAUTH() (string, bool) {
	if len(cnx.password) > 0 {
		return cnx.password, true
	}
	return "", false
}

// Reconnect force a reconnection on the given address
// is the adress is not part of the map, act like Add
func (cnx *AdminConnections) Reconnect(ctx context.Context, addr string) error {
	cnx.log.Info(fmt.Sprintf("reconnecting to %s", addr))
	cnx.Remove(ctx, addr)
	return cnx.Add(ctx, addr)
}

// AddAll connect to the given list of addresses and
// register them in the map
// fail silently
func (cnx *AdminConnections) AddAll(ctx context.Context, addrs []string) {
	for _, addr := range addrs {
		cnx.Add(ctx, addr)
	}
}

// ReplaceAll clear the pool and re-populate it with new connections
// fail silently
func (cnx *AdminConnections) ReplaceAll(ctx context.Context, addrs []string) {
	cnx.Reset(ctx)
	cnx.AddAll(ctx, addrs)
}

// Reset close all connections and clear the connection map
func (cnx *AdminConnections) Reset(ctx context.Context) {
	for _, c := range cnx.clients {
		c.Close()
	}
	cnx.clients = map[string]*redis.Client{}
}

// GetAll returns a map of all clients per address
func (cnx *AdminConnections) GetAll() map[string]*redis.Client {
	return cnx.clients
}

// GetSelected returns a map of clients based on the input addresses
func (cnx *AdminConnections) GetSelected(addrs []string) map[string]*redis.Client {
	clientsSelected := make(map[string]*redis.Client)
	for _, addr := range addrs {
		if client, ok := cnx.clients[addr]; ok {
			clientsSelected[addr] = client
		}
	}
	return clientsSelected
}

// Close used to close all possible resources instanciate by the Connections
func (cnx *AdminConnections) Close(ctx context.Context) {
	for _, c := range cnx.clients {
		c.Close()
	}
}

// Add connect to the given address and
// register the client connection to the map
func (cnx *AdminConnections) Add(ctx context.Context, addr string) error {
	_, err := cnx.Update(ctx, addr)
	return err
}

// Remove disconnect and remove the client connection from the map
func (cnx *AdminConnections) Remove(ctx context.Context, addr string) {
	if c, ok := cnx.clients[addr]; ok {
		c.Close()
		delete(cnx.clients, addr)
	}
}

func NewClient(ctx context.Context, addr string, password string, cnxTimeout time.Duration, commandsMapping map[string]string) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{
		Addr:       addr,
		Password:   password,
		Username:   "admin",
		ClientName: "default",
	})
	ping := client.Ping(ctx)
	err := fmt.Errorf("unable to PING %s", addr)
	if ping == nil {
		return nil, err
	}
	return client, nil
}

// Update returns a client connection for the given adress,
// connects if the connection is not in the map yet
func (cnx *AdminConnections) Update(ctx context.Context, addr string) (*redis.Client, error) {
	// if already exist close the current connection
	if c, ok := cnx.clients[addr]; ok {
		c.Close()
	}

	c, err := cnx.connect(ctx, addr)
	if err == nil && c != nil {
		cnx.clients[addr] = c
	} else {
		cnx.log.Info(fmt.Sprintf("cannot connect to %s ", addr))
	}
	return c, err
}

// Get returns a client connection for the given adress,
// connects if the connection is not in the map yet
func (cnx *AdminConnections) Get(ctx context.Context, addr string) (*redis.Client, error) {
	if c, ok := cnx.clients[addr]; ok {
		return c, nil
	}
	c, err := cnx.connect(ctx, addr)
	if err == nil && c != nil {
		cnx.clients[addr] = c
	}
	return c, err
}

// GetRandom returns a client connection to a random node of the client map
func (cnx *AdminConnections) GetRandom() (*redis.Client, error) {
	_, c, err := cnx.getRandomKeyClient()
	return c, err
}

// GetDifferentFrom returns random a client connection different from given address
func (cnx *AdminConnections) GetDifferentFrom(addr string) (*redis.Client, error) {
	if len(cnx.clients) == 1 {
		for a, c := range cnx.clients {
			if a != addr {
				return c, nil
			}
			return nil, errors.New(ErrNotFound)
		}
	}

	for {
		a, c, err := cnx.getRandomKeyClient()
		if err != nil {
			return nil, err
		}
		if a != addr {
			return c, nil
		}
	}
}

// GetRandom returns a client connection to a random node of the client map
func (cnx *AdminConnections) getRandomKeyClient() (string, *redis.Client, error) {
	nbClient := len(cnx.clients)
	if nbClient == 0 {
		return "", nil, errors.New(ErrNotFound)
	}
	randNumber := rand.Intn(nbClient)
	for k, c := range cnx.clients {
		if randNumber == 0 {
			return k, c, nil
		}
		randNumber--
	}

	return "", nil, errors.New(ErrNotFound)
}

// handleError handle a network error, reconnects if necessary, in that case, returns true
func (cnx *AdminConnections) handleError(ctx context.Context, addr string, err error) bool {
	if err == nil {
		return false
	} else if netError, ok := err.(net.Error); ok && netError.Timeout() {
		// timeout, reconnect
		cnx.Reconnect(ctx, addr)
		return true
	}
	switch err.(type) {
	case *net.OpError:
		// connection refused, reconnect
		cnx.Reconnect(ctx, addr)
		return true
	}
	return false
}

func (cnx *AdminConnections) connect(ctx context.Context, addr string) (*redis.Client, error) {
	c, err := NewClient(ctx, addr, cnx.password, cnx.connectionTimeout, cnx.commandsMapping)
	// ping := c.Ping(ctx)
	// res, err := ping.Result()
	// fmt.Println("Res", res, "Err", err)
	// if cnx.clientName != "" {
	// 	resp := c.Do(ctx, "CLIENT", "SETNAME", cnx.clientName)
	// 	val, err := resp.Result()
	// 	cnx.log.Info("OUT", "Resp", val, "Err", err)
	// 	return nil, cnx.ValidateResp(ctx, resp, addr, "Unable to run command CLIENT SETNAME")
	// }
	return c, err
}

// // ValidateResp check the redis resp, eventually reconnect on connection error
// // in case of error, customize the error, log it and return it
func (cnx *AdminConnections) ValidateResp(ctx context.Context, resp *redis.Cmd, addr, errMessage string) error {
	if resp == nil {
		cnx.log.Error(fmt.Errorf("%s: unable to connect to node %s", errMessage, addr), "")
		return fmt.Errorf("%s: unable to connect to node %s", errMessage, addr)
	}
	if resp.Err() != nil {
		cnx.handleError(ctx, addr, resp.Err())
		cnx.log.Error(resp.Err(), fmt.Sprintf("%s: unexpected error on node %s", errMessage, addr))
		err := fmt.Errorf("%s: unexpected error on node %s: %d", errMessage, addr, resp.Err())
		return err
	}
	return nil
}

// // ValidatePipeResp wait for all answers in the pipe and validate the response
// // in case of network issue clear the pipe and return
// // in case of error, return false
func (cnx *AdminConnections) ValidatePipeResp(ctx context.Context, resp []redis.Cmder, addr string, err error, errMessage string) bool {
	if resp == nil {
		cnx.log.Error(fmt.Errorf("%s: unable to connect to node %s", errMessage, addr), "")
		return false
	}
	if err != nil {
		cnx.log.Error(fmt.Errorf("%s: unexpected error on node %s: %v", errMessage, addr, err), "")
		if cnx.handleError(ctx, addr, err) {
			// network error, no need to continue
			return false
		}
	}
	return true
}
