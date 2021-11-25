package authz

import (
	"github.com/gofiber/fiber/v2"
	"regexp"
)

type Config struct {
	Next       func(c *fiber.Ctx) bool //skip this middleware on this condition
	RouteMatch *regexp.Regexp
	Backend    string
	//if backend is unavaiable , allow or denied
	//default denied
	Failover        bool
	IncludeBody     bool
	BodySizeInBytes int
}

var ConfigDefault = Config{
	Next:            nil,
	RouteMatch:      nil,
	Backend:         "",
	Failover:        false,
	IncludeBody:     false,
	BodySizeInBytes: 0,
}
