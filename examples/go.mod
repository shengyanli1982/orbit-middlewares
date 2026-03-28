module github.com/shengyanli1982/orbit-middlewares/examples

go 1.21

require (
	github.com/gin-gonic/gin v1.10.0
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/shengyanli1982/orbit v0.0.0
	github.com/shengyanli1982/orbit-middlewares v0.0.0
	golang.org/x/time v0.5.0
)

replace (
	github.com/shengyanli1982/orbit-middlewares => ../
	github.com/shengyanli1982/orbit => github.com/shengyanli1982/orbit v0.0.0
)
