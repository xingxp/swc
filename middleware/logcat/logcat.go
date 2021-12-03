package logcat

import (
	envoy_config_core_v3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoy_data_accesslog_v3 "github.com/envoyproxy/go-control-plane/envoy/data/accesslog/v3"
	envoy_service_accesslog_v3 "github.com/envoyproxy/go-control-plane/envoy/service/accesslog/v3"
	"github.com/gofiber/fiber/v2"
	"github.com/golang/protobuf/ptypes/timestamp"
	"github.com/golang/protobuf/ptypes/wrappers"
	"github.com/sirupsen/logrus"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// 初始化ALS服务，监听日志内容
// 以1秒钟为单位发送到logcat
func makeAlsCh(client envoy_service_accesslog_v3.AccessLogServiceClient) chan<- *envoy_data_accesslog_v3.HTTPAccessLogEntry {
	var signals []os.Signal = []os.Signal{syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGINT}
	var ch = make(chan *envoy_data_accesslog_v3.HTTPAccessLogEntry)

	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, signals...)
		var mu sync.Mutex
		var entries = []*envoy_data_accesslog_v3.HTTPAccessLogEntry{}
		var lastTime = time.Now()
		//循环等待消息
		for {
			select {
			case <-c:
				logrus.Info("[swc] logcat terminating...")
				close(ch)
				return
			case entry := <-ch:
				//放入缓存
				entries = append(entries, entry)
				if time.Now().Unix()-lastTime.Unix() >= 1 {
					mu.Lock()
					batchSendLogs(client, makeALSMessage(entries))
					entries = nil
					lastTime = time.Now()
					mu.Unlock()
				}
			}
		}
	}()

	return ch
}

// 根据当前缓冲区创建需要发送的日志消息
func makeALSMessage(entries []*envoy_data_accesslog_v3.HTTPAccessLogEntry) *envoy_service_accesslog_v3.StreamAccessLogsMessage {
	msg := &envoy_service_accesslog_v3.StreamAccessLogsMessage{
		Identifier: &envoy_service_accesslog_v3.StreamAccessLogsMessage_Identifier{
			Node: &envoy_config_core_v3.Node{
				Id:      "SWC_WEB",
				Cluster: "SWC_CLUSTER",
			},
			LogName: "ALS",
		},
		LogEntries: &envoy_service_accesslog_v3.StreamAccessLogsMessage_HttpLogs{
			HttpLogs: &envoy_service_accesslog_v3.StreamAccessLogsMessage_HTTPAccessLogEntries{
				LogEntry: entries,
			},
		},
	}
	return msg
}

// 批量发送日志
func batchSendLogs(client envoy_service_accesslog_v3.AccessLogServiceClient, entries *envoy_service_accesslog_v3.StreamAccessLogsMessage) {
	if client == nil {
		logrus.Error("client is NIL")
		return
	}
	stream, err := client.StreamAccessLogs(context.Background())
	if err != nil {
		return
	}
	defer stream.CloseSend()
	_ = stream.Send(entries)
}

func New(config ...Config) fiber.Handler {
	cfg := ConfigDefault
	if len(config) > 0 {
		cfg = config[0]
		if cfg.Backend == "" {
			cfg.Backend = ConfigDefault.Backend
		}
	}

	var (
		once sync.Once
		//mu         sync.Mutex
		errHandler fiber.ErrorHandler
		//stream     envoy_service_accesslog_v3.AccessLogService_StreamAccessLogsClient
	)

	cp := grpc.ConnectParams{
		Backoff:           backoff.DefaultConfig,
		MinConnectTimeout: 5 * time.Second,
	}
	conn, err := grpc.Dial(cfg.Backend, grpc.WithInsecure(), grpc.WithConnectParams(cp))
	if err != nil {
		logrus.Error(err)
	}
	client := envoy_service_accesslog_v3.NewAccessLogServiceClient(conn)
	//streamClient, _ := client.StreamAccessLogs(context.Background())
	logrus.Infof("[swc] Accesslog service enabled at %s, status: %s", cfg.Backend, conn.GetState())
	var ch = makeAlsCh(client)
	return func(c *fiber.Ctx) (err error) {
		once.Do(func() {
			// override error handler
			errHandler = c.App().ErrorHandler
		})

		// Handle request, store err for logging
		chainErr := c.Next()
		// Manually call error handler
		if chainErr != nil {
			if err := errHandler(c, chainErr); err != nil {
				_ = c.SendStatus(fiber.StatusInternalServerError)
			}
		}

		port64, _ := strconv.ParseUint(c.Port(), 10, 32)
		port32 := uint32(port64)
		var requestHeader = make(map[string]string)
		var responseHeader = make(map[string]string)
		c.Request().Header.VisitAll(func(key, value []byte) {
			requestHeader[string(key)] = string(value)
		})
		c.Response().Header.VisitAll(func(key, value []byte) {
			responseHeader[string(key)] = string(value)
		})
		entry := &envoy_data_accesslog_v3.HTTPAccessLogEntry{
			CommonProperties: &envoy_data_accesslog_v3.AccessLogCommon{
				SampleRate:              0,
				DownstreamRemoteAddress: nil,
				DownstreamLocalAddress:  nil,
				TlsProperties:           nil,
				StartTime: &timestamp.Timestamp{
					Seconds: time.Now().Unix(),
					Nanos:   int32(time.Now().Nanosecond()),
				},
				TimeToLastRxByte:               nil,
				TimeToFirstUpstreamTxByte:      nil,
				TimeToLastUpstreamTxByte:       nil,
				TimeToFirstUpstreamRxByte:      nil,
				TimeToLastUpstreamRxByte:       nil,
				TimeToFirstDownstreamTxByte:    nil,
				TimeToLastDownstreamTxByte:     nil,
				UpstreamRemoteAddress:          nil,
				UpstreamLocalAddress:           nil,
				UpstreamCluster:                "",
				ResponseFlags:                  nil,
				Metadata:                       nil,
				UpstreamTransportFailureReason: "",
				RouteName:                      "",
				DownstreamDirectRemoteAddress:  nil,
				FilterStateObjects:             nil,
			},
			ProtocolVersion: envoy_data_accesslog_v3.HTTPAccessLogEntry_HTTP11,
			Request: &envoy_data_accesslog_v3.HTTPRequestProperties{
				RequestMethod: MethodString2Int32(c.Method()),
				Scheme:        c.Protocol(),
				Authority:     c.Hostname(),
				Port: &wrappers.UInt32Value{
					Value: port32,
				},
				Path:                c.Path(),
				UserAgent:           c.Get(fiber.HeaderUserAgent),
				Referer:             c.Get(fiber.HeaderReferer),
				ForwardedFor:        c.Get(fiber.HeaderXForwardedFor),
				RequestId:           c.Get(fiber.HeaderXRequestID),
				OriginalPath:        c.OriginalURL(),
				RequestHeadersBytes: uint64(len(c.Request().Header.RawHeaders())),
				RequestBodyBytes:    uint64(len(c.Request().Body())),
				RequestHeaders:      requestHeader,
			},
			Response: &envoy_data_accesslog_v3.HTTPResponseProperties{
				ResponseCode: &wrappers.UInt32Value{
					Value: uint32(c.Response().StatusCode()),
				},
				ResponseHeadersBytes: uint64(len(c.Response().Header.Header())),
				ResponseBodyBytes:    uint64(len(c.Response().Body())),
				ResponseHeaders:      responseHeader,
				ResponseTrailers:     nil,
				ResponseCodeDetails:  "",
			},
		}

		//logrus.Info("产生日志")
		ch <- entry
		//buf := bytebufferpool.Get()
		//_, _ = buf.WriteString("*******")
		//mu.Lock()
		//os.Stderr.Write(buf.Bytes())
		//mu.Unlock()

		//bytebufferpool.Put(buf)
		return nil
	}
}

func MethodString2Int32(method string) envoy_config_core_v3.RequestMethod {
	switch method {
	case fiber.MethodGet:
		return envoy_config_core_v3.RequestMethod_GET
	case fiber.MethodHead:
		return envoy_config_core_v3.RequestMethod_HEAD
	case fiber.MethodPost:
		return envoy_config_core_v3.RequestMethod_POST
	case fiber.MethodPut:
		return envoy_config_core_v3.RequestMethod_PUT
	case fiber.MethodDelete:
		return envoy_config_core_v3.RequestMethod_DELETE
	case fiber.MethodConnect:
		return envoy_config_core_v3.RequestMethod_CONNECT
	case fiber.MethodOptions:
		return envoy_config_core_v3.RequestMethod_OPTIONS
	case fiber.MethodTrace:
		return envoy_config_core_v3.RequestMethod_TRACE
	case fiber.MethodPatch:
		return envoy_config_core_v3.RequestMethod_PATCH

	}
	return envoy_config_core_v3.RequestMethod_METHOD_UNSPECIFIED
}
