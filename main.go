package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/stianeikeland/go-rpio/v4"
)

// const PumpOffCrondFile = "/etc/cron.d/pumpoff"
// const PumpOffCronIntervals = "15,30,45 *"
// const PumpOffCrond = "%s * * * root curl -X POST http://localhost%s/ -d 'poweron=false'\n"

var (
	pumpLockFile           string = "pump.running.lock"
	pumpMaxPowerOnDuration time.Duration
	pumpRelayPin           int
	httpPort               string

	pumpOnCrondFile     string = "/etc/cron.d/pumpon"
	pumpOnCronIntervals string
	pumpOnCrond         string = "%s * * * root curl -X POST http://localhost%s/ -d 'poweron=true'\n"
)

func main() {
	fmt.Printf("%s: starting pumpswitch ...\n", time.Now().Format("2006-01-02 15:04:05"))

	flag.IntVar(&pumpRelayPin, "pin", 18, "RPIO relay pin")
	flag.StringVar(&pumpOnCronIntervals, "pcron", "0 0,6,12,18", "crond intervals")
	flag.DurationVar(&pumpMaxPowerOnDuration, "cycle", 15*60*time.Second, "poweron cycle")
	flag.StringVar(&httpPort, "http", ":8111", "HTTP listen addr")
	flag.Parse()

	fmt.Printf("RPIO relay pin = %d\n", pumpRelayPin)
	fmt.Printf("listen on = %s\n", httpPort)
	fmt.Printf("cron intervals =  %s\n", pumpOnCronIntervals)
	fmt.Printf("max cycle duration = %s\n", pumpMaxPowerOnDuration)

	defer rpio.Close()
	if err := rpio.Open(); err != nil {
		fmt.Printf("%s: ERR: %s\n", time.Now(), err)
		os.Exit(1)
	}

	pump, perr := newPump(pumpRelayPin, httpPort)

	if perr != nil {
		fmt.Printf("%s: ERR: %s\n", time.Now(), perr)
		os.Exit(1)
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigs
		fmt.Printf("%s: stopping pumpswitch ...\n", time.Now().Format("2006-01-02 15:04:05"))
		pump.PowerOff()
		rpio.Close()
		os.Exit(0)
	}()

	setupHTTP(pump)

	http.ListenAndServe(httpPort, nil)
}

type pump struct {
	Mutex     *sync.Mutex
	Pin       rpio.Pin
	listen    string
	StopTimer *time.Timer
	log       []string
}

func newPump(pin int, httpPort string) (*pump, error) {
	var err error
	p := &pump{Mutex: &sync.Mutex{}, Pin: rpio.Pin(pin), listen: httpPort}
	if p.IsOn() {
		if p.PowerOnDuration() <= 0*time.Second {
			fmt.Println("poweroff pump due to unknonw start time")
			p.PowerOff()
		}
	}

	go func() {
		ticker := time.NewTicker(10 * time.Second)
		for {
			<-ticker.C
			if !p.IsOn() {
				continue
			}

			if p.PowerOnDuration() > pumpMaxPowerOnDuration {
				fmt.Printf("poweroff pump at %s\n", time.Now())
				p.PowerOff()
			}
		}
	}()

	return p, err
}

func (p *pump) PowerOnDuration() time.Duration {
	p.Mutex.Lock()
	defer p.Mutex.Unlock()

	_, err := os.Stat(pumpLockFile)
	if os.IsNotExist(err) {
		return 0
	}

	content, err := ioutil.ReadFile(pumpLockFile)
	if err != nil {
		fmt.Println("poweroff pump due to err opening lockfile")
		p.Mutex.Unlock()
		p.PowerOff()
		p.DisablePowerOnCron()
		panic(err)
	}

	i, err := strconv.ParseInt(string(content), 10, 64)
	if err != nil {
		p.Mutex.Unlock()
		p.PowerOff()
		panic(err)
	}

	t := time.Unix(i, 0)

	return time.Now().Sub(t)
}

func (p *pump) EnablePowerOnCron(intervals string) error {
	p.Mutex.Lock()
	defer p.Mutex.Unlock()
	_, err := os.Stat(pumpOnCrondFile)
	if os.IsNotExist(err) {
		txt := fmt.Sprintf(pumpOnCrond, intervals, p.listen)
		return ioutil.WriteFile(pumpOnCrondFile, []byte(txt), 0644)
	}

	return nil
}

func (p *pump) DisablePowerOnCron() error {
	p.Mutex.Lock()
	defer p.Mutex.Unlock()
	return os.Remove(pumpOnCrondFile)
}

func (p *pump) PowerOnCronIsDisabled() bool {
	p.Mutex.Lock()
	defer p.Mutex.Unlock()
	_, err := os.Stat(pumpOnCrondFile)
	return os.IsNotExist(err)
}

func (p *pump) SaveLockFile(unixts int64) error {
	txt := fmt.Sprintf("%d", unixts)
	return ioutil.WriteFile(pumpLockFile, []byte(txt), 0644)
}

func (p *pump) ClearLockFile() error {
	return os.Remove(pumpLockFile)
}

func (p *pump) AddLog(txt string) {
	if len(p.log) > 100 {
		p.log = p.log[1:]
	}

	p.log = append(p.log, fmt.Sprintf("%s: %s", txt, time.Now()))
}

func (p *pump) GetLog() string {
	p.Mutex.Lock()
	defer p.Mutex.Unlock()
	return strings.Join(p.log, "\n")
}

func (p *pump) IsOn() bool {
	p.Mutex.Lock()
	defer p.Mutex.Unlock()
	res := p.Pin.Read() // Read state from pin (High / Low)
	return res == 1
}

func (p *pump) PowerOn() error {
	return p.SetTo("on")
}

func (p *pump) PowerOff() error {
	return p.SetTo("off")
}

func (p *pump) SetTo(pstate string) error {
	p.Mutex.Lock()
	defer p.Mutex.Unlock()

	p.Pin.Output()

	if pstate == "on" {
		err := p.SaveLockFile(time.Now().Unix())

		if err != nil {
			fmt.Println(err)
			return err
		}
		p.Pin.High()
		p.AddLog("pump start")
	} else if pstate == "off" {
		p.Pin.Low()
		p.ClearLockFile()
		p.AddLog("pump stop")
	}

	return nil
}

func setupHTTP(pump *pump) {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		header := fmt.Sprintf("Помпа ключ: %s <br>", time.Now().Format("2006-01-02 15:04:05"))
		header += fmt.Sprintf(
			"Продължителност на цикъла: %s<br><br>",
			pumpMaxPowerOnDuration,
		)
		errs := ""

		var isOn, isInCycle bool
		if r.Method == "POST" {
			r.ParseForm()
			var err error

			if val, ok := r.Form["poweron"]; ok {
				if val[0] == "true" {
					err = pump.PowerOn()
				} else {
					err = pump.PowerOff()
				}
			}

			if err != nil {
				errs += err.Error() + "<br>"
			}

			if val, ok := r.Form["cycleon"]; ok {
				if val[0] == "true" {
					err = pump.EnablePowerOnCron(pumpOnCronIntervals)
				} else {
					err = pump.DisablePowerOnCron()
				}
			}

			if err != nil {
				errs += err.Error() + "<br>"
			}

			if len(errs) == 0 {
				http.Redirect(w, r, r.URL.Path, 301)
			}
		}

		isOn = pump.IsOn()
		pumpOnOffColor := "#C0C0C0"
		pumpOnOffTxt := "Пусни"

		if isOn {
			pondur := pump.PowerOnDuration()
			pumpOnOffColor = "#FF0000"
			pumpOnOffTxt = "Спри"
			pumpOnOffTxt += fmt.Sprintf(" (работи от %s)", pondur.Truncate(time.Second))
		}

		isInCycle = !pump.PowerOnCronIsDisabled()
		cycleColor := "#C0C0C0"
		cycleTxt := "Активирай периодично пускане"
		if isInCycle {
			cycleColor = "#58D68D"
			cycleTxt = "Спри периодично пускане: " + pumpOnCronIntervals
		}

		html := `
		<html>
		<head>
			<meta name="viewport" content="width=device-width, initial-scale=1.0">
		</head>
		<body>%s <div style="color:red; margin:2px">%s</div> %s		<pre>%s</pre></body>
		</html>
		`

		button := `
		<form method="POST">
			<input type="submit" style="width:340px; height:140px; background-color:%s; white-space: break-spaces;" value="%s" name="startstop">
			<input type="hidden" name="poweron" value="%v">
		</form>
		<form method="POST">
			<input type="submit" style="width:340px; height:140px; background-color:%s; white-space: break-spaces;" value="%s" name="cycle">
			<input type="hidden" name="cycleon" value="%v">
		</form>
		`
		button = fmt.Sprintf(button, pumpOnOffColor, pumpOnOffTxt, !isOn, cycleColor, cycleTxt, !isInCycle)
		html = fmt.Sprintf(html, header, errs, button, pump.GetLog())
		fmt.Fprintf(w, html)
	})
}
