package redis_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/go-redis/redis/v7"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

type redisHookError struct {
	redis.Hook
}

var _ redis.Hook = redisHookError{}

func (redisHookError) BeforeProcess(ctx context.Context, cmd redis.Cmder) (context.Context, error) {
	return ctx, nil
}

func (redisHookError) AfterProcess(ctx context.Context, cmd redis.Cmder) error {
	return errors.New("hook error")
}

func TestHookError(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{
		Addr: ":6379",
	})
	rdb.AddHook(redisHookError{})

	err := rdb.Ping(ctx).Err()
	if err == nil {
		t.Fatalf("got nil, expected an error")
	}

	wanted := "hook error"
	if err.Error() != wanted {
		t.Fatalf(`got %q, wanted %q`, err, wanted)
	}
}

//------------------------------------------------------------------------------

var _ = Describe("Client", func() {
	var client *redis.Client

	BeforeEach(func() {
		client = redis.NewClient(redisOptions())
		Expect(client.FlushDB(ctx).Err()).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		client.Close()
	})

	It("should Stringer", func() {
		Expect(client.String()).To(Equal("Redis<:6380 db:15>"))
	})

	It("supports context", func() {
		ctx, cancel := context.WithCancel(ctx)
		cancel()

		err := client.Ping(ctx).Err()
		Expect(err).To(MatchError("context canceled"))
	})

	It("supports WithTimeout", func() {
		err := client.ClientPause(ctx, time.Second).Err()
		Expect(err).NotTo(HaveOccurred())

		err = client.WithTimeout(10 * time.Millisecond).Ping(ctx).Err()
		Expect(err).To(HaveOccurred())

		err = client.Ping(ctx).Err()
		Expect(err).NotTo(HaveOccurred())
	})

	It("should ping", func() {
		val, err := client.Ping(ctx).Result()
		Expect(err).NotTo(HaveOccurred())
		Expect(val).To(Equal("PONG"))
	})

	It("should return pool stats", func() {
		Expect(client.PoolStats()).To(BeAssignableToTypeOf(&redis.PoolStats{}))
	})

	It("should support custom dialers", func() {
		custom := redis.NewClient(&redis.Options{
			Network: "tcp",
			Addr:    redisAddr,
			Dialer: func(ctx context.Context, network, addr string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, network, addr)
			},
		})

		val, err := custom.Ping(ctx).Result()
		Expect(err).NotTo(HaveOccurred())
		Expect(val).To(Equal("PONG"))
		Expect(custom.Close()).NotTo(HaveOccurred())
	})

	It("should close", func() {
		Expect(client.Close()).NotTo(HaveOccurred())
		err := client.Ping(ctx).Err()
		Expect(err).To(MatchError("redis: client is closed"))
	})

	It("should close pubsub without closing the client", func() {
		pubsub := client.Subscribe(ctx)
		Expect(pubsub.Close()).NotTo(HaveOccurred())

		_, err := pubsub.Receive(ctx)
		Expect(err).To(MatchError("redis: client is closed"))
		Expect(client.Ping(ctx).Err()).NotTo(HaveOccurred())
	})

	It("should close Tx without closing the client", func() {
		err := client.Watch(ctx, func(tx *redis.Tx) error {
			_, err := tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.Ping(ctx)
				return nil
			})
			return err
		})
		Expect(err).NotTo(HaveOccurred())

		Expect(client.Ping(ctx).Err()).NotTo(HaveOccurred())
	})

	It("should close pipeline without closing the client", func() {
		pipeline := client.Pipeline()
		Expect(pipeline.Close()).NotTo(HaveOccurred())

		pipeline.Ping(ctx)
		_, err := pipeline.Exec(ctx)
		Expect(err).To(MatchError("redis: client is closed"))

		Expect(client.Ping(ctx).Err()).NotTo(HaveOccurred())
	})

	It("should close pubsub when client is closed", func() {
		pubsub := client.Subscribe(ctx)
		Expect(client.Close()).NotTo(HaveOccurred())

		_, err := pubsub.Receive(ctx)
		Expect(err).To(MatchError("redis: client is closed"))

		Expect(pubsub.Close()).NotTo(HaveOccurred())
	})

	It("should close pipeline when client is closed", func() {
		pipeline := client.Pipeline()
		Expect(client.Close()).NotTo(HaveOccurred())
		Expect(pipeline.Close()).NotTo(HaveOccurred())
	})

	It("should select DB", func() {
		db2 := redis.NewClient(&redis.Options{
			Addr: redisAddr,
			DB:   2,
		})
		Expect(db2.FlushDB(ctx).Err()).NotTo(HaveOccurred())
		Expect(db2.Get(ctx, "db").Err()).To(Equal(redis.Nil))
		Expect(db2.Set(ctx, "db", 2, 0).Err()).NotTo(HaveOccurred())

		n, err := db2.Get(ctx, "db").Int64()
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(Equal(int64(2)))

		Expect(client.Get(ctx, "db").Err()).To(Equal(redis.Nil))

		Expect(db2.FlushDB(ctx).Err()).NotTo(HaveOccurred())
		Expect(db2.Close()).NotTo(HaveOccurred())
	})

	It("processes custom commands", func() {
		cmd := redis.NewCmd(ctx, "PING")
		_ = client.Process(ctx, cmd)

		// Flush buffers.
		Expect(client.Echo(ctx, "hello").Err()).NotTo(HaveOccurred())

		Expect(cmd.Err()).NotTo(HaveOccurred())
		Expect(cmd.Val()).To(Equal("PONG"))
	})

	It("should retry command on network error", func() {
		Expect(client.Close()).NotTo(HaveOccurred())

		client = redis.NewClient(&redis.Options{
			Addr:       redisAddr,
			MaxRetries: 1,
		})

		// Put bad connection in the pool.
		cn, err := client.Pool().Get(ctx)
		Expect(err).NotTo(HaveOccurred())

		cn.SetNetConn(&badConn{})
		client.Pool().Put(cn)

		err = client.Ping(ctx).Err()
		Expect(err).NotTo(HaveOccurred())
	})

	It("should retry with backoff", func() {
		clientNoRetry := redis.NewClient(&redis.Options{
			Addr:       ":1234",
			MaxRetries: 0,
		})
		defer clientNoRetry.Close()

		clientRetry := redis.NewClient(&redis.Options{
			Addr:            ":1234",
			MaxRetries:      5,
			MaxRetryBackoff: 128 * time.Millisecond,
		})
		defer clientRetry.Close()

		startNoRetry := time.Now()
		err := clientNoRetry.Ping(ctx).Err()
		Expect(err).To(HaveOccurred())
		elapseNoRetry := time.Since(startNoRetry)

		startRetry := time.Now()
		err = clientRetry.Ping(ctx).Err()
		Expect(err).To(HaveOccurred())
		elapseRetry := time.Since(startRetry)

		Expect(elapseRetry).To(BeNumerically(">", elapseNoRetry, 10*time.Millisecond))
	})

	It("should update conn.UsedAt on read/write", func() {
		cn, err := client.Pool().Get(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(cn.UsedAt).NotTo(BeZero())
		createdAt := cn.UsedAt()

		client.Pool().Put(cn)
		Expect(cn.UsedAt().Equal(createdAt)).To(BeTrue())

		time.Sleep(time.Second)

		err = client.Ping(ctx).Err()
		Expect(err).NotTo(HaveOccurred())

		cn, err = client.Pool().Get(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(cn).NotTo(BeNil())
		Expect(cn.UsedAt().After(createdAt)).To(BeTrue())
	})

	It("should process command with special chars", func() {
		set := client.Set(ctx, "key", "hello1\r\nhello2\r\n", 0)
		Expect(set.Err()).NotTo(HaveOccurred())
		Expect(set.Val()).To(Equal("OK"))

		get := client.Get(ctx, "key")
		Expect(get.Err()).NotTo(HaveOccurred())
		Expect(get.Val()).To(Equal("hello1\r\nhello2\r\n"))
	})

	It("should handle big vals", func() {
		bigVal := bytes.Repeat([]byte{'*'}, 2e6)

		err := client.Set(ctx, "key", bigVal, 0).Err()
		Expect(err).NotTo(HaveOccurred())

		// Reconnect to get new connection.
		Expect(client.Close()).NotTo(HaveOccurred())
		client = redis.NewClient(redisOptions())

		got, err := client.Get(ctx, "key").Bytes()
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(bigVal))
	})
})

var _ = Describe("Client timeout", func() {
	var opt *redis.Options
	var client *redis.Client

	AfterEach(func() {
		Expect(client.Close()).NotTo(HaveOccurred())
	})

	testTimeout := func() {
		It("Ping timeouts", func() {
			err := client.Ping(ctx).Err()
			Expect(err).To(HaveOccurred())
			Expect(err.(net.Error).Timeout()).To(BeTrue())
		})

		It("Pipeline timeouts", func() {
			_, err := client.Pipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.Ping(ctx)
				return nil
			})
			Expect(err).To(HaveOccurred())
			Expect(err.(net.Error).Timeout()).To(BeTrue())
		})

		It("Subscribe timeouts", func() {
			if opt.WriteTimeout == 0 {
				return
			}

			pubsub := client.Subscribe(ctx)
			defer pubsub.Close()

			err := pubsub.Subscribe(ctx, "_")
			Expect(err).To(HaveOccurred())
			Expect(err.(net.Error).Timeout()).To(BeTrue())
		})

		It("Tx timeouts", func() {
			err := client.Watch(ctx, func(tx *redis.Tx) error {
				return tx.Ping(ctx).Err()
			})
			Expect(err).To(HaveOccurred())
			Expect(err.(net.Error).Timeout()).To(BeTrue())
		})

		It("Tx Pipeline timeouts", func() {
			err := client.Watch(ctx, func(tx *redis.Tx) error {
				_, err := tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
					pipe.Ping(ctx)
					return nil
				})
				return err
			})
			Expect(err).To(HaveOccurred())
			Expect(err.(net.Error).Timeout()).To(BeTrue())
		})
	}

	Context("read timeout", func() {
		BeforeEach(func() {
			opt = redisOptions()
			opt.ReadTimeout = time.Nanosecond
			opt.WriteTimeout = -1
			client = redis.NewClient(opt)
		})

		testTimeout()
	})

	Context("write timeout", func() {
		BeforeEach(func() {
			opt = redisOptions()
			opt.ReadTimeout = -1
			opt.WriteTimeout = time.Nanosecond
			client = redis.NewClient(opt)
		})

		testTimeout()
	})
})

var _ = Describe("Client OnConnect", func() {
	var client *redis.Client

	BeforeEach(func() {
		opt := redisOptions()
		opt.DB = 0
		opt.OnConnect = func(cn *redis.Conn) error {
			return cn.ClientSetName(ctx, "on_connect").Err()
		}

		client = redis.NewClient(opt)
	})

	AfterEach(func() {
		Expect(client.Close()).NotTo(HaveOccurred())
	})

	It("calls OnConnect", func() {
		name, err := client.ClientGetName(ctx).Result()
		Expect(err).NotTo(HaveOccurred())
		Expect(name).To(Equal("on_connect"))
	})
})
