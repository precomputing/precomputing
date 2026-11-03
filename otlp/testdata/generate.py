# Writes the OTLP fixtures the tests read, with the official OpenTelemetry protobuf classes, so the
# hand-written decoders are checked against the reference encoder rather than against themselves.
#
#   python3 -m venv /tmp/otel && /tmp/otel/bin/pip install opentelemetry-proto
#   /tmp/otel/bin/python otlp/testdata/generate.py otlp/testdata
#
# Each request is written twice: NAME.pb in protobuf and NAME.json in OTLP's JSON encoding.
import sys
from pathlib import Path

from google.protobuf import json_format
from opentelemetry.proto.collector.logs.v1 import logs_service_pb2 as ls
from opentelemetry.proto.collector.metrics.v1 import metrics_service_pb2 as ms
from opentelemetry.proto.common.v1 import common_pb2 as c
from opentelemetry.proto.logs.v1 import logs_pb2 as l
from opentelemetry.proto.metrics.v1 import metrics_pb2 as m
from opentelemetry.proto.resource.v1 import resource_pb2 as r

out = Path(sys.argv[1])
NS = 1_000_000_000
T = 1790586000  # 2026-09-28 09:00 UTC


def kv(k, v):
    a = c.AnyValue()
    if isinstance(v, bool):
        a.bool_value = v
    elif isinstance(v, int):
        a.int_value = v
    elif isinstance(v, float):
        a.double_value = v
    elif isinstance(v, bytes):
        a.bytes_value = v
    elif isinstance(v, list):
        for x in v:
            a.array_value.values.add().CopyFrom(kv("", x).value)
    elif isinstance(v, dict):
        for kk, vv in v.items():
            a.kvlist_value.values.add().CopyFrom(kv(kk, vv))
    else:
        a.string_value = v
    return c.KeyValue(key=k, value=a)


def res(**attrs):
    return r.Resource(attributes=[kv(k.replace("_", "."), v) for k, v in attrs.items()])


def write(name, msg):
    (out / (name + ".pb")).write_bytes(msg.SerializeToString())
    # OTLP/JSON: lowerCamelCase names, 64-bit integers as strings, enums as numbers.
    (out / (name + ".json")).write_text(json_format.MessageToJson(msg, use_integers_for_enums=True, indent=None) + "\n")


# Logs: two services; the second record has only an observed time, the third none at all.
req = ls.ExportLogsServiceRequest()
rl = req.resource_logs.add(resource=res(service_name="checkout", host_name="h1"))
sl = rl.scope_logs.add(scope=c.InstrumentationScope(name="shop.payments", version="1.2"))
sl.log_records.add(time_unix_nano=T * NS + 500_000_000, severity_number=17,
                   body=c.AnyValue(string_value="payment failed order=81 ms=120"),
                   attributes=[kv("http.route", "/pay"), kv("user.id", 42)])
sl.log_records.add(observed_time_unix_nano=(T + 1) * NS, severity_text="INFO",
                   body=c.AnyValue(string_value="payment ok order=82 ms=31"),
                   attributes=[kv("http.route", "/pay"), kv("cart", ["a", 2, True]), kv("extra", {"k": 1.5})])
sl.log_records.add(severity_text="ERROR", body=c.AnyValue(string_value="no time at all ms=5"),
                   attributes=[kv("http.route", "/pay")])
rl = req.resource_logs.add(resource=res(service_name="web"))
sl = rl.scope_logs.add()
sl.log_records.add(time_unix_nano=(T + 2) * NS, severity_text="ERROR",
                   body=c.AnyValue(string_value="upstream timeout ms=3000"),
                   attributes=[kv("http.route", "/search"), kv("raw", b"\x00\x01")])
write("logs", req)


def metrics(t, total, hist_count, hist_sum, start=T - 600):
    req = ms.ExportMetricsServiceRequest()
    rm = req.resource_metrics.add(resource=res(service_name="api"))
    sm = rm.scope_metrics.add(scope=c.InstrumentationScope(name="http"))
    g = sm.metrics.add(name="queue.depth")
    g.gauge.data_points.add(time_unix_nano=t * NS, as_int=5, attributes=[kv("queue", "a")])
    g.gauge.data_points.add(time_unix_nano=t * NS, as_double=2.5, attributes=[kv("queue", "b")])
    s = sm.metrics.add(name="http.requests")
    s.sum.aggregation_temporality = m.AGGREGATION_TEMPORALITY_CUMULATIVE
    s.sum.is_monotonic = True
    s.sum.data_points.add(start_time_unix_nano=start * NS, time_unix_nano=t * NS, as_int=total,
                          attributes=[kv("http.route", "/a")])
    h = sm.metrics.add(name="http.server.request.duration")
    h.histogram.aggregation_temporality = m.AGGREGATION_TEMPORALITY_DELTA
    h.histogram.data_points.add(start_time_unix_nano=(t - 60) * NS, time_unix_nano=t * NS, count=hist_count,
                                sum=hist_sum, min=0.05, max=0.2, bucket_counts=[1, 2, 1],
                                explicit_bounds=[0.1, 0.15], attributes=[kv("http.route", "/a")])
    q = sm.metrics.add(name="rpc.duration")
    q.summary.data_points.add(time_unix_nano=t * NS, count=3, sum=1.0)
    e = sm.metrics.add(name="db.duration")
    e.exponential_histogram.data_points.add(time_unix_nano=t * NS, count=2, sum=0.3)
    sm.metrics.add(name="not.taken").gauge.data_points.add(time_unix_nano=t * NS, as_double=1)
    return req


# Three exports of the same series: a first one, a later one, then one after the process restarted
# (a new start time and a smaller total).
write("metrics-1", metrics(T, 10, 4, 0.5))
write("metrics-2", metrics(T + 60, 25, 2, 0.25))
write("metrics-3", metrics(T + 120, 3, 1, 0.1, start=T + 100))
