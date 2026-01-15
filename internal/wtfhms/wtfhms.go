package wtfhms

import (
	"flag"
	"fmt"
	"sort"
	"time"
)

// Notes
// simply tailored for our one specific case
// generalizations will can be done later

var (
	timeRange          = 1
	endtime            = time.Now().Format("15:04")
	topIPsCount        = 5
	IPclass            = "D"
	log_2_analyze      *Log2Analyze
	file2parse         = "/var/log/httpd/ssl_access_log"
	date_layout        = "02/Jan/2006:15:04:05 -0700"
	date2analyze       = time.Now().Format("2006-01-02")
	ip_adress string
	not_ip string
	log_type           = "apache"
	log_format         = "%h %l %u %t \"%r\" %>s %O \"%{Referer}i\" \"%{User-Agent}i\""
)

type TopIPs struct {
	TimeRange []string
	TotalRequests int
	RequestsPerSecond int
	
}



func sort_by_rcount(IP_rcount map[string]int) map[string]int {
	var output string
	// maps are not ordered, so we need to sort the map by the request count
	entries := len(IP_rcount)
	if entries > 0 {
		// first step: get all the keys from the map into a slice, that can be sorted
		ips := make([]string, 0, entries)
		for ip := range IP_rcount {
			ips = append(ips, ip)
		}
		// second step: sort the slice by the request count
		sort.SliceStable(ips, func(i, j int) bool {
			return IP_rcount[ips[i]] > IP_rcount[ips[j]]
		})
		// third step: iterate over the sorted slice and print the ip and the request count
		for _, ip := range ips {
			output += "\t" + ip + "\t: " + fmt.Sprintf("%v", IP_rcount[ip]) + "\n"
		}
	}
	return output
}



func wtfhms() (){
	pst := time.Now()

	log_2_analyze = new(Log2Analyze)
	
	LogIt.Info("setting date to analyze to " + log_2_analyze.Date2analyze)
	fmt.Println("setting date to analyze to " + log_2_analyze.Date2analyze)

	fmt.Println("output is written to", config.OutputFolder)
	// start working
	log_2_analyze.FileName = config.DefaultFile2analyze

	log_2_analyze.RetrieveEntries(*endtime, *timeRange)

	top_ips, code_count := log_2_analyze.GetTopIPs()

	// print output
	infos := make(map[string]string)
	var timestamps []string
	infos["Total requests"] = fmt.Sprintf("%v", log_2_analyze.EntryCount)
	if *timeRange != 0 {
		timestamps = append(timestamps, log_2_analyze.StartTime.Format("2006-01-02 15:04"))
		timestamps = append(timestamps, log_2_analyze.EndTime.Format("2006-01-02 15:04"))
		infos["Requests per second"] = fmt.Sprintf("%v", log_2_analyze.EntryCount/(*timeRange*60))
	}
	if log_2_analyze.QueryString != "" {
		infos["query string"] = log_2_analyze.QueryString
	}

	header := BuildOutputHeader(log_2_analyze.FileName, time.Now().Local().Format("20060102_150405"), timestamps, infos)
	fmt.Println(header)
	LogIt.Info(header)
	sorted_ips := sort_by_rcount(top_ips)
	fmt.Println("")
	fmt.Println("\tTop IPs\t\t: count")
	fmt.Println("\t------------------------------")
	fmt.Println(sorted_ips)
	LogIt.Info(sorted_ips)
	fmt.Printf("finished in %v\n", time.Since(pst))
}
