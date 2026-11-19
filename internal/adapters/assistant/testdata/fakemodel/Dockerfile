# The live test's fake model (main.go). Build context: this directory.
FROM golang:1.26-alpine AS build
COPY main.go /src/
RUN cd /src && go mod init fakemodel >/dev/null && CGO_ENABLED=0 go build -o /fakemodel .
FROM scratch
COPY --from=build /fakemodel /fakemodel
ENTRYPOINT ["/fakemodel"]
