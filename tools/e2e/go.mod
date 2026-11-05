module ridepulse/tools/e2e

go 1.25.6

require (
	github.com/segmentio/kafka-go v0.4.50
	ridepulse/pkg v0.0.0
)

require (
	github.com/klauspost/compress v1.15.9 // indirect
	github.com/pierrec/lz4/v4 v4.1.15 // indirect
)

replace ridepulse/pkg => ../../pkg
