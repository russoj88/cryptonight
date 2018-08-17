package cryptonight

//go:generate cpp -o cryptonight_gen.go -P -undef -nostdinc -traditional -Wall cryptonight.go -imacros $GOFILE
//go:generate gofmt -w cryptonight_gen.go

const _ = `
#define build
#define ignore
#define HEAD_PLACEHOLDER Code generated by cpp. DO NOT EDIT.
`
