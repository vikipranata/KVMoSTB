module kvmostb

go 1.24.1

require (
	github.com/gorilla/websocket v1.5.3
	github.com/vladimirvivien/go4vl v0.0.5
)

require golang.org/x/sys v0.0.0-20210630005230-0f9fa26af87c // indirect

// Dependencies are vendored as source under third_party/, so building needs no network.
replace (
	github.com/gorilla/websocket => ./third_party/websocket
	github.com/vladimirvivien/go4vl => ./third_party/go4vl
	golang.org/x/sys => ./third_party/xsys
)
