FROM golang:1.21.11

ENV GO111MODULE=on

WORKDIR apps/p2p

COPY go.mod .
COPY go.sum .

RUN go mod download

COPY . .

RUN go build -o /usr/local/bin/p2p -v ./main/main.go

CMD ["p2p"]
