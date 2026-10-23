// Package useragent parses an HTTP User-Agent header into the browser,
// operating system and device type recorded with a sign-in (domain §5.8).
// It follows the names of the DeviceDetector library lago uses (lago
// Utils::DeviceInfo): the browser is "<client> <version>", the operating
// system a DeviceDetector OS name, and the device type a DeviceDetector
// type, "desktop" when none is detected. It covers the common browsers,
// operating systems and HTTP libraries with a fixed set of rules, without
// DeviceDetector's regular expression database.
package useragent
