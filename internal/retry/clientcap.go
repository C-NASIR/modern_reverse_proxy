package retry

import (
	"container/list"
	"net"
	"net/http"
	"strings"
	"sync"

	"modern_reverse_proxy/internal/policy"
)

const anonymousClient = "anonymous"

type ClientCap struct {
	mu      sync.Mutex
	percent int
	burst   int
	lruSize int
	items   map[string]*list.Element
	order   *list.List
}

type clientBucket struct {
	key    string
	budget *Budget
}

func NewClientCap(percent int, burst int, lruSize int) *ClientCap {
	return &ClientCap{
		percent: percent,
		burst:   burst,
		lruSize: lruSize,
		items:   make(map[string]*list.Element),
		order:   list.New(),
	}
}

func (c *ClientCap) Bucket(key string) *Budget {
	if c == nil {
		return nil
	}
	if key == "" {
		key = anonymousClient
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if element, ok := c.items[key]; ok {
		c.order.MoveToFront(element)
		return element.Value.(*clientBucket).budget
	}

	bucket := &clientBucket{key: key, budget: NewBudget(c.percent, c.burst)}
	element := c.order.PushFront(bucket)
	c.items[key] = element

	if c.lruSize > 0 && c.order.Len() > c.lruSize {
		oldest := c.order.Back()
		if oldest != nil {
			c.order.Remove(oldest)
			oldBucket := oldest.Value.(*clientBucket)
			delete(c.items, oldBucket.key)
		}
	}

	return bucket.budget
}

func ClientKey(req *http.Request, policy policy.ClientRetryCapPolicy) string {
	if req == nil {
		return anonymousClient
	}
	key := strings.TrimSpace(policy.Key)
	if key == "" {
		return anonymousClient
	}

	if strings.EqualFold(key, "ip") {
		if host, _, err := net.SplitHostPort(req.RemoteAddr); err == nil && host != "" {
			return host
		}
		if req.RemoteAddr != "" {
			return req.RemoteAddr
		}
		return anonymousClient
	}

	lower := strings.ToLower(key)
	if strings.HasPrefix(lower, "header:") {
		headerName := strings.TrimSpace(key[len("header:"):])
		if headerName == "" {
			return anonymousClient
		}
		value := req.Header.Get(headerName)
		if value == "" {
			return anonymousClient
		}
		return value
	}

	return anonymousClient
}
