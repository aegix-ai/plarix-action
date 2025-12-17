.PHONY: build update-pricing clean

build:
	go build -o plarix ./cmd/plarix

update-pricing:
	go run ./cmd/update-pricing

clean:
	rm -f plarix plarix_Linux_x86_64.tar.gz
