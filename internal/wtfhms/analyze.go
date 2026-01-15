package wtfhms

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

type LogEntry struct {
	IP        string
	Class     string
	TimeStamp time.Time
	Method    string
	Request   string
	Code      int
	RTime     string
}

type Log2Analyze struct {
	FileName     string
	DateLayout   string
	StartTime    time.Time
	EndTime      time.Time
	Date2analyze string
	QueryString  string
	Entries      []LogEntry
	EntryCount   int
}

var placeholder_lut = make(map[string]int)

func fill_placeholder_lut() {
	// create lut for the log entry's parts
	// doesn't work for user agent strings, as we cannot foresee the length
	placeholders := strings.Fields(config.LogFormat)
	LogIt.Debug("found the following placeholders: " + strings.Join(placeholders[:], ","))

	tspos := slices.Index(placeholders, "%t")
	if tspos != -1 {
		placeholders = slices.Insert(placeholders, tspos+1, "ts2")
		placeholders = slices.Replace(placeholders, tspos, tspos+1, "ts1")
	}
	rpos := slices.Index(placeholders, "%r")
	if rpos != -1 {
		placeholders = slices.Insert(placeholders, rpos+1, "r", "p")
		placeholders = slices.Replace(placeholders, rpos, rpos+1, "m")
	}
	LogIt.Debug("will create lut with the placeholders: " + strings.Join(placeholders[:], ","))
	for i, placeholder := range placeholders {
		placeholder_lut[placeholder] = i
	}
}

func (l *Log2Analyze) RetrieveEntries(endtime string, timerange int) {
	// retrieves the records from the log file within a time range or of the whole file
	// if no timerange is given

	fill_placeholder_lut()

	file, err := os.Open(l.FileName)
	if err != nil {
		LogIt.Debug("Error opening file: " + l.FileName)
		fmt.Println("Error opening file: " + l.FileName)
		log.Fatal(err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file) // scan the contents of a file and print line by line
	c := 0
	cl := 0
	for scanner.Scan() {
		line := scanner.Text()
		cl++
		entry := create_entry(line)
		// StartTime is zero, if this is the first entry
		if l.StartTime.IsZero() {
			// TODO: check the date layout and stopp the loop, if the date layout is not correct

			// set the start and end time according to the date of the first entry
			LogIt.Debug("l.StartTime is zero, setting Start and End Time")
			l.StartTime, l.EndTime = create_time_range(endtime, timerange, l.Date2analyze)
			LogIt.Debug("Start Time: " + l.StartTime.Format(log_2_analyze.DateLayout))
			LogIt.Debug("End Time: " + l.EndTime.Format(log_2_analyze.DateLayout))
		}
		// check to avoid crash
		entry_ip := ""
		if len(entry.IP) < len(*ip_adress) {
			entry_ip = entry.IP
		} else {
			entry_ip = entry.IP[0:len(*ip_adress)]
		}
		entry_nip := ""
		if len(entry.IP) < len(*not_ip) {
			entry_nip = entry.IP
		} else {
			entry_nip = entry.IP[0:len(*not_ip)]
		}
		if (timerange == 0 || entry.Between(l.StartTime, l.EndTime)) &&
			(*ip_adress == "" || entry_ip == *ip_adress) &&
			(*not_ip == "" || entry_nip != *not_ip) &&
			(*response_code == 0 || entry.Code == *response_code) &&
			(*no_response_code == 0 || entry.Code != *no_response_code) {
			if strings.Contains(entry.Request, l.QueryString) || l.QueryString == "" {
				l.Entries = append(l.Entries, entry)
				c++
			}
		}
		// if the timerange is zero, we set the endtime to the last entry
		if timerange == 0 {
			l.EndTime = entry.TimeStamp
		}
	}
	l.EntryCount = len(l.Entries)
	LogIt.Info("checked " + fmt.Sprintf("%d", cl) + " lines")
	LogIt.Info("found Entries within timerange: " + fmt.Sprintf("%v", l.EntryCount))
	LogIt.Debug(" counter says: " + fmt.Sprintf("%d", c))

	if err := scanner.Err(); err != nil {
		fmt.Println("Error reading from file:", err) // print error if scanning is not done properly
	}
}

func (l Log2Analyze) GetTopIPs() (map[string]int, map[int]int) {
	// returns the five ip addresses with the highest request count within the last 5 minutes
	ip_count := make(map[string]int)
	code_count := make(map[int]int)
	for _, record := range l.Entries {
		ip_count[record.Class]++
		code_count[record.Code]++
	}

	// to prevent it from crashing, when the given map ip_count, we check the length
	// of the map and if it is empty, we return an empty map
	// if the map is not empty, we sort the map by the request count and return the top 5 or less
	top_ips := make(map[string]int)
	entries := len(ip_count)
	if entries > *topIPsCount && *topIPsCount > 0 {
		entries = *topIPsCount
	}
	if entries > 0 {
		// build a slice of the keys of the map, so we can sort it to get the top 5
		ips := make([]string, 0, entries)
		for ip := range ip_count {
			ips = append(ips, ip)
		}
		// sort the slice by the request count
		sort.SliceStable(ips, func(i, j int) bool {
			return ip_count[ips[i]] > ip_count[ips[j]]
		})
		// get the top 5 or less, if there are less than 5 entries
		// be aware, that the resulting map (top_ips) is not ordered!
		for i := 0; i < entries; i++ {
			top_ips[ips[i]] = ip_count[ips[i]]
		}
	}
	return top_ips, code_count
}

func (l Log2Analyze) WriteOutputFiles(top_ips map[string]int, code_counts map[int]int) {
	// one file per IP, if a number of topIPs is requested
	if *topIPsCount > 0 {
		if *combined_file {
			cfile, err := os.Create(config.OutputFolder + "combined-" + time.Now().Local().Format("20060102_150405") + ".txt")
			if err != nil {
				log.Fatal(err)
			}
			defer cfile.Close()
			infos := make(map[string]string)
			var timestamps []string
			infos["Total requests"] = fmt.Sprintf("%v", l.EntryCount)
			if *timeRange != 0 {
				timestamps = append(timestamps, l.StartTime.Format("2006-01-02 15:04"))
				timestamps = append(timestamps, l.EndTime.Format("2006-01-02 15:04"))
				infos["Requests per second"] = fmt.Sprintf("%v", l.EntryCount/(*timeRange*60))
			}
			if l.QueryString != "" {
				infos["query string"] = l.QueryString
			}

			header := BuildOutputHeader(l.FileName, time.Now().Local().Format("20060102_150405"), timestamps, infos)

			cfile.WriteString(header)

			if *topIPsCount < 31 {
				cfile.WriteString("\n\tTop IPs\t\t: count")
				cfile.WriteString("\n\t------------------------------\n")
				cfile.WriteString(sort_by_rcount(top_ips))
			}

			cfile.WriteString("\n")
			for ip, count := range top_ips {
				cfile.WriteString("\n")
				cfile.WriteString(ip + "\t" + "=> " + fmt.Sprintf("%v", count) + " requests\n")
				cfile.WriteString("==================================================================\n")
				for _, record := range l.Entries {
					if record.Class == ip {
						cfile.WriteString(record.TimeStamp.Format(l.DateLayout) + "\t" + record.IP + "\t" + record.Method + "\t" + record.Request + "\t" + fmt.Sprintf("%d", record.Code) + "\t" + record.RTime + "\n")
					}
				}
			}
		} else {
			for ip, count := range top_ips {
				file, err := os.Create(config.OutputFolder + fmt.Sprintf("%05d", count) + "_" + ip + ".txt")
				if err != nil {
					log.Fatal(err)
				}
				defer file.Close()
				file.WriteString(ip + "\t" + fmt.Sprintf("%v", count) + "\n")
				for _, record := range l.Entries {
					if record.Class == ip {
						file.WriteString(record.TimeStamp.Format(l.DateLayout) + "\t" + record.IP + "\t" + record.Method + "\t" + record.Request + "\t" + fmt.Sprintf("%d", record.Code) + "\t" + record.RTime + "\n")
					}
				}
			}
		}
	} else { // if all IPs are requested (-n 0)
		file, err := os.Create(config.OutputFolder + "ip-list.txt")
		if err != nil {
			log.Fatal(err)
		}
		defer file.Close()
		// sort IPs
		ips := make([]string, 0, len(top_ips))
		for ip := range top_ips {
			ips = append(ips, ip)
		}
		sort.SliceStable(ips, func(i, j int) bool {
			return top_ips[ips[i]] > top_ips[ips[j]]
		})
		for _, ip := range ips {
			file.WriteString(ip + "\t" + fmt.Sprintf("%v", top_ips[ip]) + "\n")
		}

	}
	file, err := os.Create(config.OutputFolder + "response_codes-" + time.Now().Local().Format("20060102_150405") + ".txt")
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()
	file.WriteString("Code\tCount\n====\t=======\n")
	// maps are not ordered, so we need to sort the map by the request count
	entries := len(code_counts)
	if entries > 0 {
		// first step: get all the keys from the map into a slice, that can be sorted
		codes := make([]int, 0, entries)
		for code := range code_counts {
			codes = append(codes, code)
		}
		// second step: sort the slice by the request count
		sort.SliceStable(codes, func(i, j int) bool {
			return code_counts[codes[i]] > code_counts[codes[j]]
		})
		// third step: iterate over the sorted slice and print the code and the request count
		for _, c := range codes {
			file.WriteString(fmt.Sprintf("%d\t%d", c, code_counts[c]) + "\n")
		}
	}
}

func (e LogEntry) Between(start, end time.Time) bool {
	// checks if the LogEntry is between the start and end time
	passt := e.TimeStamp.After(start) && e.TimeStamp.Before(end)

	// the following line produces heavy debug output
	// LogIt.Debug(start.Format(log_2_analyze.DateLayout) + " > " + e.TimeStamp.Format(log_2_analyze.DateLayout) + " > " + end.Format(log_2_analyze.DateLayout) + " :: " + fmt.Sprintf("%v", passt))
	return passt
}

func create_entry(line string) LogEntry {
	// creates a LogEntry from a line
	var ip, class, method, request, rtime string
	var code int
	var timestamp time.Time

	switch config.LogType {
	case "apache_atmire":
		ip, class, timestamp, method, request, code, rtime = parse_apache_atmire(line)
	case "logfmt":
		ip, class, timestamp, method, request, code, rtime = parse_log(line)
	case "apache":
		ip, class, timestamp, method, request, code, rtime = parse_apache(line)
	case "rosetta":
		ip, class, timestamp, method, request, code, rtime = parse_rosetta(line)
	}
	return LogEntry{
		IP:        ip,
		Class:     class,
		TimeStamp: timestamp,
		Method:    method,
		Request:   request,
		Code:      code,
		RTime:     rtime,
	}
}

func parse_apache_atmire(line string) (string, string, time.Time, string, string, int, string) {
	var ip, class, method, request, codestring, rtime string
	var code int
	var timestamp time.Time
	var err error
	parts := strings.Split(strings.Replace(line, "\"", "", -1), " ")
	timestring := strings.Replace(parts[3], "[", "", 1) + " " + strings.Replace(parts[4], "]", "", 1)
	timestamp, err = time.Parse(log_2_analyze.DateLayout, timestring)
	if err != nil {
		LogIt.Error("Error parsing timestamp: " + timestring + " with layout " + log_2_analyze.DateLayout)
		LogIt.Error("Error parsing timestamp: " + err.Error())
	}
	// crude workaround for short lines
	if len(parts) < 9 {
		LogIt.Debug("having difficulties to parse line: " + line)
		LogIt.Debug("got " + fmt.Sprintf("%d", len(parts)) + " parts")
		LogIt.Debug("got parts: " + fmt.Sprintf("%v", parts))
		for i := 0; i < (9 - len(parts)); i++ {
			parts = append(parts, "")
		}
	}
	ip = parts[0]
	method = parts[5]
	request = parts[6]
	// crude workaround for lines with response code 408
	if parts[8] == "-" {
		codestring = parts[6]
		request = parts[5]
	} else {
		codestring = parts[8]
	}
	code, err = strconv.Atoi(codestring)
	if err != nil {
		LogIt.Error("Error parsing code (maybe hacking?): " + codestring)
		LogIt.Error(line)
		LogIt.Debug("Error parsing code: " + err.Error())
		code = 0
	}

	// switch to get the IP class
	ip_parts := strings.Split(ip, ".")
	if len(ip_parts) == 4 {
		switch *IPclass {
		case "A":
			class = ip_parts[0]
		case "B":
			class = ip_parts[0] + "." + ip_parts[1]
		case "C":
			class = ip_parts[0] + "." + ip_parts[1] + "." + ip_parts[2]
		case "D":
			class = ip
		default:
			class = ip
		}
	} else {
		class = ip
	}
	return ip, class, timestamp, method, request, code, rtime
}

func parse_apache(line string) (string, string, time.Time, string, string, int, string) {
	var ip, class, method, request, codestring, rtime string
	var code int
	var timestamp time.Time
	var err error
	parts := strings.Split(strings.Replace(line, "\"", "", -1), " ")
	timestring := strings.Replace(parts[3], "[", "", 1) + " " + strings.Replace(parts[4], "]", "", 1)
	timestamp, err = time.Parse(log_2_analyze.DateLayout, timestring)
	if err != nil {
		LogIt.Error("Error parsing timestamp: " + timestring + " with layout " + log_2_analyze.DateLayout)
		LogIt.Error("Error parsing timestamp: " + err.Error())
	}
	// crude workaround for short lines
	if len(parts) < 9 {
		LogIt.Debug("having difficulties to parse line: " + line)
		LogIt.Debug("got " + fmt.Sprintf("%d", len(parts)) + " parts")
		LogIt.Debug("got parts: " + fmt.Sprintf("%v", parts))
		for i := 0; i < (9 - len(parts)); i++ {
			parts = append(parts, "")
		}
	}
	ip = parts[0]
	method = parts[5]
	request = parts[6]
	// crude workaround for lines with response code 408
	if parts[8] == "-" {
		codestring = parts[6]
		request = parts[5]
	} else {
		codestring = parts[8]
	}
	code, err = strconv.Atoi(codestring)
	if err != nil {
		LogIt.Error("Error parsing code (maybe hacking?): " + codestring)
		LogIt.Error(line)
		LogIt.Debug("Error parsing code: " + err.Error())
		code = 0
	}
	// switch to get the IP class
	ip_parts := strings.Split(ip, ".")
	if len(ip_parts) == 4 {
		switch *IPclass {
		case "A":
			class = ip_parts[0]
		case "B":
			class = ip_parts[0] + "." + ip_parts[1]
		case "C":
			class = ip_parts[0] + "." + ip_parts[1] + "." + ip_parts[2]
		case "D":
			class = ip
		default:
			class = ip
		}
	} else {
		class = ip
	}
	return ip, class, timestamp, method, request, code, rtime
}

func parse_rosetta(line string) (string, string, time.Time, string, string, int, string) {
	var ip, class, method, request, codestring, rtime string
	var code int
	var timestamp time.Time
	var err error
	parts := strings.Split(strings.Replace(line, "\"", "", -1), " ")
	timestring := strings.Replace(parts[4], "[", "", 1) + " " + strings.Replace(parts[5], "]", "", 1)
	timestamp, err = time.Parse(log_2_analyze.DateLayout, timestring)
	if err != nil {
		LogIt.Error("Error parsing timestamp: " + timestring + " with layout " + log_2_analyze.DateLayout)
		LogIt.Error("Error parsing timestamp: " + err.Error())
	}
	// crude workaround for short lines
	if len(parts) < 9 {
		LogIt.Debug("having difficulties to parse line: " + line)
		LogIt.Debug("got " + fmt.Sprintf("%d", len(parts)) + " parts")
		LogIt.Debug("got parts: " + fmt.Sprintf("%v", parts))
		for i := 0; i < (9 - len(parts)); i++ {
			parts = append(parts, "")
		}
	}
	if parts[1] == "-" {
		ip = parts[0]
	} else {
		ip = parts[1]
	}
	method = parts[6]
	request = parts[7]
	// crude workaround for lines with response code 408
	if parts[9] == "-" {
		codestring = parts[7]
		request = parts[6]
	} else {
		codestring = parts[9]
	}
	code, err = strconv.Atoi(codestring)
	if err != nil {
		LogIt.Error("Error parsing code (maybe hacking?): " + codestring)
		LogIt.Error(line)
		LogIt.Debug("Error parsing code: " + err.Error())
		code = 0
	}
	// switch to get the IP class
	ip_parts := strings.Split(ip, ".")
	switch *IPclass {
	case "A":
		class = ip_parts[0]
	case "B":
		class = ip_parts[0] + "." + ip_parts[1]
	case "C":
		class = ip_parts[0] + "." + ip_parts[1] + "." + ip_parts[2]
	case "D":
		class = ip
	default:
		class = ip
	}
	return ip, class, timestamp, method, request, code, rtime
}

func parse_log(line string) (string, string, time.Time, string, string, int, string) {
	var ip, class, method, request, rtime string
	var code int
	var timestamp time.Time
	var err error

	parts := strings.Split(strings.Replace(line, "\"", "", -1), " ")
	timestring := strings.Replace(parts[placeholder_lut["ts1"]], "[", "", 1) + " " + strings.Replace(parts[placeholder_lut["ts2"]], "]", "", 1)
	timestamp, err = time.Parse(log_2_analyze.DateLayout, timestring)
	if err != nil {
		LogIt.Error("Error parsing timestamp: " + timestring + " with layout " + log_2_analyze.DateLayout)
		LogIt.Error("Error parsing timestamp: " + err.Error())
	}
	// crude workaround for short lines
	if len(parts) < 9 {
		LogIt.Debug("having difficulties to parse line: " + line)
		LogIt.Debug("got " + fmt.Sprintf("%d", len(parts)) + " parts")
		LogIt.Debug("got parts: " + fmt.Sprintf("%v", parts))
		for i := 0; i < (9 - len(parts)); i++ {
			parts = append(parts, "")
		}
	}

	ip = parts[placeholder_lut["%h"]]
	method = parts[placeholder_lut["m"]]
	request = parts[placeholder_lut["r"]]
	code, err = strconv.Atoi(parts[placeholder_lut["%>s"]])
	if err != nil {
		LogIt.Error("Error parsing status code: " + parts[placeholder_lut["%>s"]])
		code = 0
	}

	// switch to get the IP class
	ipParts := strings.Split(ip, ".")
	if len(ipParts) == 4 {
		switch *IPclass {
		case "A":
			class = ipParts[0]
		case "B":
			class = ipParts[0] + "." + ipParts[1]
		case "C":
			class = ipParts[0] + "." + ipParts[1] + "." + ipParts[2]
		case "D":
			class = ip
		default:
			class = ip
		}
	} else {
		class = ip
	}
	return ip, class, timestamp, method, request, code, rtime
}

func create_time_range(endtimestring string, timerange int, date2analyze string) (time.Time, time.Time) {
	// creates a time range to analyze the log file
	// the range is defined by the time2analyze flag
	// Will man ein anderes Datum parsen, muss man das per Schlater übergeben!
	LogIt.Debug("got End Time String: " + endtimestring)
	LogIt.Debug("got Time Range: " + fmt.Sprintf("%d", timerange))
	// enrich the endtimestring with the current date and timezone
	endtimestring = fmt.Sprintf("%s %s:00 %s", date2analyze, endtimestring, time.Now().Local().Format("Z0700"))
	LogIt.Debug("End Time String: " + endtimestring)
	// create a time object from the endtimestring
	endtime, _ := time.Parse("2006-01-02 15:04:05 -0700", endtimestring)
	LogIt.Debug("created End Time: " + endtime.Format(log_2_analyze.DateLayout))

	var starttime time.Time
	if timerange > 0 {
		// create a starttime by subtracting the timerange from the endtime
		starttime = endtime.Add(time.Duration(-timerange) * time.Minute)
	}
	LogIt.Debug("created Start Time( - " + fmt.Sprintf("%d", timerange) + "): " + starttime.Format(log_2_analyze.DateLayout))
	return starttime, endtime
}

// func count_requests(records []LogEntry) map[string]int {
// 	// counts the requests per ip
// 	ip_count := make(map[string]int)
// 	for _, record := range records {
// 		ip_count[record.IP]++
// 	}
// 	return ip_count
// }
//
// func count_codes(records []LogEntry) map[string]int {
// 	// counts the status codes
// 	code_count := make(map[string]int)
// 	for _, record := range records {
// 		code_count[record.Code]++
// 	}
// 	return code_count
// }
