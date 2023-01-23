FROM golang:1.12 as builder
WORKDIR /go/src/github.com/EncoreTechnologies/prometheus-puppetdb-exporter
COPY . .
RUN make prometheus-puppetdb-exporter

FROM scratch
COPY --from=builder /go/src/github.com/EncoreTechnologies/prometheus-puppetdb-exporter/prometheus-puppetdb-exporter /
ENTRYPOINT ["/prometheus-puppetdb-exporter"]
CMD [""]
