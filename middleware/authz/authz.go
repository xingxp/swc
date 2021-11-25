package authz

import (
	envoy_config_core_v3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoy_service_auth_v3 "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/utils"
	"github.com/golang/protobuf/ptypes/timestamp"
	"github.com/sirupsen/logrus"
	"github.com/valyala/fasthttp"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"strconv"
	"time"
)

func New(config ...Config) fiber.Handler {
	cfg := ConfigDefault

	// Override config if provided
	if len(config) > 0 {
		cfg = config[0]
		// Set default values
		if cfg.Next == nil {
			cfg.Next = ConfigDefault.Next
		}
		if cfg.Backend == "" {
			cfg.Backend = ConfigDefault.Backend
		}
		if cfg.RouteMatch == nil {
			cfg.RouteMatch = ConfigDefault.RouteMatch
		}
		if cfg.IncludeBody && cfg.BodySizeInBytes <= 0 {
			cfg.BodySizeInBytes = ConfigDefault.BodySizeInBytes
		}

	}
	logrus.Infof("[swc] External authZ service enabled at %s", cfg.Backend)
	return func(c *fiber.Ctx) error {

		if cfg.Next != nil && cfg.Next(c) {
			return c.Next()
		}

		if cfg.RouteMatch == nil || cfg.Backend == "" {
			return c.Next()
		}
		//判定路由是否需要验证
		if !cfg.RouteMatch.Match(c.Request().URI().Path()) {
			return c.Next()
		}
		body := ""
		if cfg.IncludeBody {
			b := utils.CopyBytes(c.Body())
			if cfg.BodySizeInBytes > len(b) {
				body = string(b)
			} else {
				body = string(b[:cfg.BodySizeInBytes])
			}
		}
		ok, err := checkRequest(cfg.Backend, &envoy_service_auth_v3.CheckRequest{Attributes: &envoy_service_auth_v3.AttributeContext{
			Source: &envoy_service_auth_v3.AttributeContext_Peer{
				Address: &envoy_config_core_v3.Address{
					Address: &envoy_config_core_v3.Address_SocketAddress{
						SocketAddress: &envoy_config_core_v3.SocketAddress{
							Protocol: envoy_config_core_v3.SocketAddress_TCP,
							Address:  c.IP(),
							PortSpecifier: &envoy_config_core_v3.SocketAddress_PortValue{
								PortValue: str2uint32(c.Port()),
							},
							ResolverName: "",
							Ipv4Compat:   false,
						},
					},
				},
				Service:     "",
				Labels:      nil,
				Principal:   "",
				Certificate: "",
			},
			Destination: nil,
			Request: &envoy_service_auth_v3.AttributeContext_Request{
				Time: &timestamp.Timestamp{
					Seconds: time.Now().Unix(),
					Nanos:   int32(time.Now().Nanosecond()),
				},
				Http: &envoy_service_auth_v3.AttributeContext_HttpRequest{
					Id:       c.Get(fiber.HeaderXRequestID),
					Method:   c.Method(),
					Headers:  getHeaderMap(c.Request().Header),
					Path:     c.Path(),
					Host:     c.Hostname(),
					Scheme:   c.Protocol(),
					Query:    string(c.Request().URI().QueryString()),
					Fragment: "",
					Size:     int64(len(body)),
					Protocol: c.Protocol(),
					Body:     body,
					//RawBody:  c.Request().Body(),
				},
			},
			ContextExtensions: nil,
			MetadataContext:   nil,
		}})
		if err != nil {
			//发生连接authz错误，但是failover放行
			if cfg.Failover {
				return c.Next()
			}
			c.SendStatus(fiber.StatusUnauthorized)
			return nil
		}
		if ok {
			return c.Next()
		}
		c.SendStatus(fiber.StatusUnauthorized)
		return nil
	}
}

//校验
func checkRequest(endpoint string, cr *envoy_service_auth_v3.CheckRequest) (bool, error) {
	cp := grpc.ConnectParams{
		Backoff:           backoff.DefaultConfig,
		MinConnectTimeout: 5 * time.Second,
	}
	conn, err := grpc.Dial(endpoint, grpc.WithInsecure(), grpc.WithConnectParams(cp))
	defer conn.Close()
	if err != nil {
		logrus.Errorf("can not communicate authZ server: %s", err)
		return false, err
	}
	client := envoy_service_auth_v3.NewAuthorizationClient(conn)
	response, err := client.Check(context.Background(), cr)
	if err != nil {
		logrus.Errorf("authz check error: %s", err)
		return false, err
	}
	switch response.GetHttpResponse().(type) {
	case *envoy_service_auth_v3.CheckResponse_OkResponse:
		logrus.Info("authZ check ALLOW - " + cr.Attributes.Request.Http.Path)
		return true, nil
	case *envoy_service_auth_v3.CheckResponse_DeniedResponse:
		logrus.Info("authZ check DENIED")
		return false, nil
	}
	//logrus.Infof(response.Status.Message)
	return false, nil
}

func str2uint32(str string) uint32 {
	s, _ := strconv.ParseUint(str, 10, 32)
	return uint32(s)
}

func getHeaderMap(h fasthttp.RequestHeader) map[string]string {
	var r = make(map[string]string)
	h.VisitAll(func(key, value []byte) {
		r[string(key)] = string(value)
	})
	return r
}
