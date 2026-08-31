module github.com/oktaforai-okta/fleetops-bifrost-demo/server

// No third-party dependencies. Token validation is RS256 over a JWKS, which is a
// SHA-256 hash and a PKCS#1 v1.5 verify, both in crypto/rsa.
go 1.27.0
