package main

// Authentication providers compiled into this binary. AUTH_PROVIDER selects
// one of them at startup; remove an import to drop a provider you do not use.
//
// To add a provider from another repository:
//
//	go get github.com/someone/latihan-auth-ldap@v1.0.0
//
// then add its import below and rebuild the image.
import (
	_ "github.com/perdhevi/latihanAPI/internal/authn/cognito"
	_ "github.com/perdhevi/latihanAPI/internal/authn/firebase"
	_ "github.com/perdhevi/latihanAPI/internal/authn/oidc"
)
