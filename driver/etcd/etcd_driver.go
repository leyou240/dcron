package etcd

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/libi/dcron/driver"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"
)

var _ driver.Driver = (*Driver)(nil)

const (
	defaultLease    = 5 // 5 second ttl
	dialTimeout     = 3 * time.Second
	businessTimeout = 5 * time.Second
)

type Driver struct {
	cli        *clientv3.Client
	lease      int64
	serverList map[string]map[string]string
	lock       sync.RWMutex
	leaseID    clientv3.LeaseID
}

// NewDriver 新建etcd的驱动
func NewDriver(cli *clientv3.Client) (*Driver, error) {
	ser := &Driver{
		cli:        cli,
		serverList: make(map[string]map[string]string, 10),
	}
	return ser, nil
}

// NewEtcdDriver ...
func NewEtcdDriver(config *clientv3.Config) (*Driver, error) {
	cli, err := clientv3.New(*config)
	if err != nil {
		return nil, err
	}

	ser := &Driver{
		cli:        cli,
		serverList: make(map[string]map[string]string, 10),
	}

	return ser, nil
}

// 设置key value，绑定租约
func (d *Driver) putKeyWithLease(key, val string) (clientv3.LeaseID, error) {
	//设置租约时间，最少5s
	if d.lease < defaultLease {
		d.lease = defaultLease
	}

	ctx, cancel := context.WithTimeout(context.Background(), businessTimeout)
	defer cancel()

	resp, err := d.cli.Grant(ctx, d.lease)
	if err != nil {
		return 0, err
	}
	//注册服务并绑定租约
	_, err = d.cli.Put(ctx, key, val, clientv3.WithLease(resp.ID))
	if err != nil {
		return 0, err
	}

	return resp.ID, nil
}

func (d *Driver) randNodeID(serviceName string) (nodeID string) {
	return getPrefix(serviceName) + uuid.New().String()
}

// WatchService 初始化服务列表和监视
func (d *Driver) watchService(serviceName string) error {
	prefix := getPrefix(serviceName)
	// 根据前缀获取现有的key
	resp, err := d.cli.Get(context.Background(), prefix, clientv3.WithPrefix())
	if err != nil {
		return err
	}

	for _, ev := range resp.Kvs {
		d.setServiceList(serviceName, string(ev.Key), string(ev.Value))
	}

	// 监视前缀，修改变更的server
	go d.watcher(serviceName)
	return nil
}

func getPrefix(serviceName string) string {
	return serviceName + "/"
}

// watcher 监听前缀
func (d *Driver) watcher(serviceName string) {
	prefix := getPrefix(serviceName)
	rch := d.cli.Watch(context.Background(), prefix, clientv3.WithPrefix())
	for wresp := range rch {
		for _, ev := range wresp.Events {
			switch ev.Type {
			case mvccpb.PUT: //修改或者新增
				d.setServiceList(serviceName, string(ev.Kv.Key), string(ev.Kv.Value))
			case mvccpb.DELETE: //删除
				d.delServiceList(serviceName, string(ev.Kv.Key))
			}
		}
	}
}

// setServiceList 新增服务地址
func (d *Driver) setServiceList(serviceName, key, val string) {
	d.lock.Lock()
	defer d.lock.Unlock()
	if nodeMap, ok := d.serverList[serviceName]; !ok {
		nodeMap = map[string]string{
			key: val,
		}
		d.serverList[serviceName] = nodeMap
	} else {
		d.serverList[serviceName][key] = val
	}
}

// DelServiceList 删除服务地址
func (d *Driver) delServiceList(serviceName, key string) {
	d.lock.Lock()
	defer d.lock.Unlock()
	if nodeMap, ok := d.serverList[serviceName]; ok {
		delete(nodeMap, key)
	}
}

// GetServices 获取服务地址
func (d *Driver) getServices(serviceName string) []string {
	d.lock.RLock()
	defer d.lock.RUnlock()
	addrs := make([]string, 0)
	if nodeMap, ok := d.serverList[serviceName]; ok {
		for _, v := range nodeMap {
			addrs = append(addrs, v)
		}
	}
	return addrs
}

func (d *Driver) Ping() error {
	return nil
}

func (d *Driver) keepAlive(ctx context.Context, nodeID string) (<-chan *clientv3.LeaseKeepAliveResponse, error) {
	var err error
	d.leaseID, err = d.putKeyWithLease(nodeID, nodeID)
	if err != nil {
		log.Printf("putKeyWithLease error: %v", err)
		return nil, err
	}

	return d.cli.KeepAlive(ctx, d.leaseID)
}

func (d *Driver) revoke() {
	_, err := d.cli.Lease.Revoke(context.Background(), d.leaseID)
	if err != nil {
		log.Printf("lease revoke error: %v", err)
	}
}

func (d *Driver) SetHeartBeat(nodeID string) {
	leaseCh, err := d.keepAlive(context.Background(), nodeID)
	if err != nil {
		log.Printf("setHeartBeat error: %v", err)
		return
	}
	go func() {
		defer func() {
			err := recover()
			if err != nil {
				log.Printf("keepAlive panic: %v", err)
				return
			}
		}()
		for {
			select {
			case _, ok := <-leaseCh:
				if !ok {
					d.revoke()
					d.SetHeartBeat(nodeID)
					return
				}
			case <-time.After(businessTimeout):
				log.Printf("ectd cli keepalive timeout")
				return
			}
		}
	}()
}

// SetTimeout set etcd lease timeout
func (d *Driver) SetTimeout(timeout time.Duration) {
	d.lease = int64(timeout.Seconds())
}

// GetServiceNodeList get service notes
func (d *Driver) GetServiceNodeList(serviceName string) ([]string, error) {
	return d.getServices(serviceName), nil
}

// RegisterServiceNode register a node to service
func (d *Driver) RegisterServiceNode(serviceName string) (string, error) {
	nodeId := d.randNodeID(serviceName)
	_, err := d.putKeyWithLease(nodeId, nodeId)
	if err != nil {
		return "", err
	}
	err = d.watchService(serviceName)
	if err != nil {
		return "", err
	}
	return nodeId, nil
}
