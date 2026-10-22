package docker

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

var (
	dockerDFSummary   = regexp.MustCompile(`^(Images|Containers|Local Volumes|Build Cache)\s+(\d+)\s+(\d+)\s+(\S+)\s+(\S+)(?:\s+\([^)]*\))?\s*$`)
	dockerSizePattern = regexp.MustCompile(`(?i)^([0-9]+(?:\.[0-9]+)?)([kmgtpe]?i?B)$`)
)

type dockerBytesValue int64

func (v *dockerBytesValue) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		parsed, parseErr := parseDockerSize(text)
		if parseErr != nil {
			return parseErr
		}
		*v = dockerBytesValue(parsed)
		return nil
	}
	var number int64
	if err := json.Unmarshal(data, &number); err != nil {
		return err
	}
	*v = dockerBytesValue(number)
	return nil
}

func parseDockerDF(output string) (DiskUsage, error) {
	usage := DiskUsage{
		Categories: []DiskCategory{{Kind: "images"}, {Kind: "containers"}, {Kind: "volumes"}, {Kind: "buildCache"}},
		Images:     []DiskImage{}, Containers: []DiskContainer{}, Volumes: []VolumeUsage{},
		Networks: []NetworkUsage{}, BuildCache: []BuildCacheUsage{},
	}
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	matched := 0
	for _, line := range lines {
		match := dockerDFSummary.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			continue
		}
		size, err := parseDockerSize(match[4])
		if err != nil {
			return DiskUsage{}, err
		}
		reclaimable, err := parseDockerSize(match[5])
		if err != nil {
			return DiskUsage{}, err
		}
		total, _ := strconv.Atoi(match[2])
		active, _ := strconv.Atoi(match[3])
		kind := map[string]string{"Images": "images", "Containers": "containers", "Local Volumes": "volumes", "Build Cache": "buildCache"}[match[1]]
		for index := range usage.Categories {
			if usage.Categories[index].Kind == kind {
				usage.Categories[index] = DiskCategory{Kind: kind, Total: total, Active: active, SizeBytes: size, ReclaimableBytes: reclaimable}
			}
		}
		matched++
	}
	if matched == 0 {
		return DiskUsage{}, fmt.Errorf("docker system df summary not found")
	}
	var err error
	if usage.Images, err = parseDFImages(lines); err != nil {
		return DiskUsage{}, err
	}
	if usage.Containers, err = parseDFContainers(lines); err != nil {
		return DiskUsage{}, err
	}
	if usage.Volumes, err = parseDFVolumes(lines); err != nil {
		return DiskUsage{}, err
	}
	if usage.BuildCache, err = parseDFBuildCache(lines); err != nil {
		return DiskUsage{}, err
	}
	return usage, nil
}

func parseDFImages(lines []string) ([]DiskImage, error) {
	header, rows := findDFSection(lines, "Images space usage:")
	if header == "" {
		return []DiskImage{}, nil
	}
	table, err := parseDFTable(header, rows, []string{"REPOSITORY", "TAG", "IMAGE ID", "CREATED", "SIZE", "SHARED SIZE", "UNIQUE SIZE", "CONTAINERS"})
	if err != nil {
		return nil, err
	}
	result := make([]DiskImage, 0, len(table))
	for _, row := range table {
		size, err := parseDockerSize(row["SIZE"])
		if err != nil {
			return nil, fmt.Errorf("parse image size: %w", err)
		}
		unique, err := parseDockerSize(row["UNIQUE SIZE"])
		if err != nil {
			return nil, fmt.Errorf("parse image unique size: %w", err)
		}
		containers, _ := strconv.Atoi(row["CONTAINERS"])
		result = append(result, DiskImage{ID: row["IMAGE ID"], Repository: row["REPOSITORY"], Tag: row["TAG"], Containers: containers, SizeBytes: size, UniqueSizeBytes: unique})
	}
	return result, nil
}

func parseDFContainers(lines []string) ([]DiskContainer, error) {
	header, rows := findDFSection(lines, "Containers space usage:")
	if header == "" {
		return []DiskContainer{}, nil
	}
	table, err := parseDFTable(header, rows, []string{"CONTAINER ID", "IMAGE", "COMMAND", "LOCAL VOLUMES", "SIZE", "CREATED", "STATUS", "NAMES"})
	if err != nil {
		return nil, err
	}
	result := make([]DiskContainer, 0, len(table))
	for _, row := range table {
		size, err := parseDockerSize(row["SIZE"])
		if err != nil {
			return nil, fmt.Errorf("parse container size: %w", err)
		}
		result = append(result, DiskContainer{ID: row["CONTAINER ID"], Name: row["NAMES"], Image: row["IMAGE"], Status: row["STATUS"], SizeBytes: size})
	}
	return result, nil
}

func parseDFVolumes(lines []string) ([]VolumeUsage, error) {
	header, rows := findDFSection(lines, "Local Volumes space usage:")
	if header == "" {
		return []VolumeUsage{}, nil
	}
	table, err := parseDFTable(header, rows, []string{"VOLUME NAME", "LINKS", "SIZE"})
	if err != nil {
		return nil, err
	}
	result := make([]VolumeUsage, 0, len(table))
	for _, row := range table {
		size, err := parseDockerSize(row["SIZE"])
		if err != nil {
			return nil, fmt.Errorf("parse volume size: %w", err)
		}
		links, _ := strconv.Atoi(row["LINKS"])
		result = append(result, VolumeUsage{Name: row["VOLUME NAME"], Links: links, SizeBytes: size, Containers: []ContainerUse{}})
	}
	return result, nil
}

func parseDFBuildCache(lines []string) ([]BuildCacheUsage, error) {
	header, rows := findDFSection(lines, "Build cache usage:")
	if header == "" {
		return []BuildCacheUsage{}, nil
	}
	table, err := parseDFTable(header, rows, []string{"CACHE ID", "CACHE TYPE", "SIZE", "CREATED", "LAST USED", "USAGE", "SHARED"})
	if err != nil {
		return nil, err
	}
	result := make([]BuildCacheUsage, 0, len(table))
	for _, row := range table {
		size, err := parseDockerSize(row["SIZE"])
		if err != nil {
			return nil, fmt.Errorf("parse build cache size: %w", err)
		}
		result = append(result, BuildCacheUsage{ID: row["CACHE ID"], Type: row["CACHE TYPE"], SizeBytes: size, Shared: row["SHARED"] == "true"})
	}
	return result, nil
}

func findDFSection(lines []string, title string) (string, []string) {
	for index, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), title) {
			continue
		}
		index++
		for index < len(lines) && strings.TrimSpace(lines[index]) == "" {
			index++
		}
		if index >= len(lines) {
			return "", nil
		}
		header := lines[index]
		index++
		rows := make([]string, 0)
		for ; index < len(lines) && strings.TrimSpace(lines[index]) != ""; index++ {
			rows = append(rows, lines[index])
		}
		return header, rows
	}
	return "", nil
}

func parseDFTable(header string, rows []string, columns []string) ([]map[string]string, error) {
	positions := make([]int, len(columns))
	offset := 0
	for index, column := range columns {
		relative := strings.Index(header[offset:], column)
		if relative < 0 {
			return nil, fmt.Errorf("docker df column %s not found in %q", column, header)
		}
		positions[index] = offset + relative
		offset = positions[index] + len(column)
	}
	result := make([]map[string]string, 0, len(rows))
	for _, line := range rows {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "---") {
			continue
		}
		row := make(map[string]string, len(columns))
		for index, column := range columns {
			start := positions[index]
			if start >= len(line) {
				row[column] = ""
				continue
			}
			end := len(line)
			if index+1 < len(positions) && positions[index+1] < end {
				end = positions[index+1]
			}
			row[column] = strings.TrimSpace(line[start:end])
		}
		result = append(result, row)
	}
	return result, nil
}

func parseDockerSize(value string) (int64, error) {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) == 0 {
		return 0, nil
	}
	match := dockerSizePattern.FindStringSubmatch(fields[0])
	if match == nil {
		return 0, fmt.Errorf("unsupported docker size %q", value)
	}
	number, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return 0, err
	}
	multipliers := map[string]float64{
		"b": 1, "kb": 1e3, "mb": 1e6, "gb": 1e9, "tb": 1e12, "pb": 1e15, "eb": 1e18,
		"kib": 1 << 10, "mib": 1 << 20, "gib": 1 << 30, "tib": 1 << 40, "pib": 1 << 50, "eib": 1 << 60,
	}
	multiplier, ok := multipliers[strings.ToLower(match[2])]
	if !ok {
		return 0, fmt.Errorf("unsupported docker size unit %q", match[2])
	}
	return int64(math.Round(number * multiplier)), nil
}
