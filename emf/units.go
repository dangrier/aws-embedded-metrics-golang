package emf

type MetricUnit string

const (
	None            MetricUnit = "None"
	Seconds         MetricUnit = "Seconds"
	Microseconds    MetricUnit = "Microseconds"
	Milliseconds    MetricUnit = "Milliseconds"
	Bytes           MetricUnit = "Bytes"
	Kilobytes       MetricUnit = "Kilobytes"
	Megabytes       MetricUnit = "Megabytes"
	Gigabytes       MetricUnit = "Gigabytes"
	Terabytes       MetricUnit = "Terabytes"
	Bits            MetricUnit = "Bits"
	Kilobits        MetricUnit = "Kilobits"
	Megabits        MetricUnit = "Megabits"
	Gigabits        MetricUnit = "Gigabits"
	Terabits        MetricUnit = "Terabits"
	Percent         MetricUnit = "Percent"
	Count           MetricUnit = "Count"
	BytesSecond     MetricUnit = "Bytes/Second"
	KilobytesSecond MetricUnit = "Kilobytes/Second"
	MegabytesSecond MetricUnit = "Megabytes/Second"
	GigabytesSecond MetricUnit = "Gigabytes/Second"
	TerabytesSecond MetricUnit = "Terabytes/Second"
	BitsSecond      MetricUnit = "Bits/Second"
	KilobitsSecond  MetricUnit = "Kilobits/Second"
	MegabitsSecond  MetricUnit = "Megabits/Second"
	GigabitsSecond  MetricUnit = "Gigabits/Second"
	TerabitsSecond  MetricUnit = "Terabits/Second"
	CountSecond     MetricUnit = "Count/Second"
)

// units lists every unit the EMF spec allows.
var units = []MetricUnit{
	None, Seconds, Microseconds, Milliseconds,
	Bytes, Kilobytes, Megabytes, Gigabytes, Terabytes,
	Bits, Kilobits, Megabits, Gigabits, Terabits,
	Percent, Count,
	BytesSecond, KilobytesSecond, MegabytesSecond, GigabytesSecond, TerabytesSecond,
	BitsSecond, KilobitsSecond, MegabitsSecond, GigabitsSecond, TerabitsSecond,
	CountSecond,
}
