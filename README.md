## Prerequisites
1) Версия golang хотя бы 1.24.0 (лучше 1.26.0 - новый оптимизированный GreenTea GC).
2) Версия ядра Linux, которая поддерживает cgroups version 2.
3) На Debian отключенный systemd-oomd Out Of Memory killer:
```bash
sudo systemctl stop systemd-oomd
sudo systemctl disable --now systemd-oomd
sudo systemctl mask systemd-oomd
```
4) Незагруженная система (выключить сервисы, которые потребляют много CPU и памяти).
5) Скачать:<br />
https://downloads.openappsec.io/waf-comparison-project/legitimate.zip<br />
https://downloads.openappsec.io/waf-comparison-project/malicious.zip<br />
6) Разархивировать и поместить обе папки в папку http-corpuses в директорию проекта (чтобы не указывать command line флаги при прогоне).
7) Для полного прогона понадобиться около 32ГБ памяти, так как при пиковой нагрузке система может потреблять до 17GB<br />
  (система перед прогоном потребляла около 2GB памяти).
8) Установить `sudo apt-get install gcc libpcre2-dev pkg-config`.

## Выбор языка
Был выбран Golang, так как он позволяет быстрее прототипировать, чем Rust.

## Инструкция по полному прогону
1) Сбилдить проект находясь в директории проекта `./compile.sh` (если надо указать GOPATH в парамeтре к сприпту).
   На выходе получится исполняемый файл `waap`.
2) Приложение имеет две команды `probe` и `export-results`:
```bash
$ ./waap --help
WAAP

Usage:
  waap [command] [flags]
  waap [command]

Available Commands:
  completion     Generate the autocompletion script for the specified shell
  export-results Exports nmap probe results to json files
  help           Help about any command
  probe          Run nmap probes against http request corpuses

Flags:
  -h, --help                       help for waap
      --pprof-server-port string   pprof server port (default "6060")

Use "waap [command] --help" for more information about a command.
```
- Команда `probe` запускает процесс probing-а http корпусов.
```bash
$ ./waap probe --help
Run nmap probes against http request corpuses

Usage:
  waap probe [flags]

Flags:
      --badger-data-dir string     Path to BadgerDB data directory (default "./badger")
      --cpu-cores-limit int        Restrict running process to a subset of available CPU cores (default 16)
      --cpu-percentage float       Restrict running process's CPU usage (default 90)
  -h, --help                       help for probe
      --http-corpuses-dir string   Path to http corpuses directory (default "./http-corpuses")
      --nmap-probes-file string    Path to nmap-service-probes file (default "./nmap-service-probes")
      --probe-num-requests int     Probe only a subset of http requests (default -1)

Global Flags:
      --pprof-server-port string   pprof server port (default "6060")

```
- Команда `export-results` экспортирует резлуьтаты прогона в json файлы в директорию `results`
```bash
$ ./waap export-results --help
Exports nmap probe results to json files

Usage:
  waap export-results [flags]

Flags:
      --badger-data-dir string    Path to BadgerDB data directory (default "./badger")
  -h, --help                      help for export-results
      --results-dir-path string   Path to results directory with json files results, will be created if not exists (default "./results")

Global Flags:
      --pprof-server-port string   pprof server port (default "6060")

```
4) Итак процесс получения результата (если не перезаписывались дефолтные значение флагов):
```bash
$ ./waap probe
$ ./waap export-results
```

6) Команда `./waap probe` выдает статиситику прогона после завершения в формате:
```bash
Processed a total of 699 http corpus streams
Processed a total of 1114166 http requests
Total time taken in minutes: 25.6629872598
AVG RPS: 723.588087822283
```

7) Формат результатов в папке `results` после запуска `./waap export-results` будет примерно таким:
```json
{
  "results": [
    {
      "http_request": "GET /cdn-cgi/challenge-platform/h/b/scripts/jsd/62ec4f065604/main.js\r\nhost: www.abt.com\r\n\r\nsec-ch-ua-platform: \"Windows\"\r\n\r\nuser-agent: Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36\r\n\r\nsec-ch-ua: \"Google Chrome\";v=\"129\", \"Not=A?Brand\";v=\"8\", \"Chromium\";v=\"129\"\r\n\r\nsec-ch-ua-mobile: ?0\r\n\r\naccept: */*\r\n\r\nsec-fetch-site: same-origin\r\n\r\nsec-fetch-mode: no-cors\r\n\r\nsec-fetch-dest: script\r\n\r\naccept-encoding: gzip, deflate, br, zstd\r\n\r\naccept-language: en-US,en;q=0.9\r\n\r\ncookie: abtVisit=217137c71e9459bb877a04b9d1f2af74; __cf_bm=rmIqDwo1EtfthMAFfEtL1jy0TsL6o9vv32_vRDrlMuk-1728663170-1.0.1.1-OrkYWYtcoxZwc0SP3XBBxEyLpOwNifv2x7a_jOQ1CORMUV.5ij1.pSOv89RsblLPyPHWBg_Sh2Ue.qHZmll37k2GYUWxzfJBi3v4Xe3ocBI\r\n\r\n",
      "null_probe": [
        {
          "probe_name": "TCP NULL",
          "matches": [
            "telnet"
          ],
          "match_count": 3897,
          "softmatches": [
            "ms-pe-exe",
            "pkzip-file",
            "fhem",
            "filezilla",
            "napster",
            "reverse-ssl"
          ],
          "softmatch_count": 72
        }
      ],
      "rarity_1": [
        {
          "probe_name": "TCP GenericLines",
          "matches": [
            "zmodem"
          ],
          "match_count": 685,
          "softmatches": [
            "http",
            "insteon-plm",
          ],
          "softmatch_count": 12
        },
        {
          "probe_name": "TCP GetRequest",
          "matches": [
            "http"
          ],
          "match_count": 4846,
          "softmatches": [
            "clickhouse",
            "docker",
            "http-proxy",
          ],
          "softmatch_count": 23
        }
      ]
    }
  ]
}
```

## Инструкция по тестовому прогону
1) Чтобы не ждать долго и просто протестировать программу можно запустить приложение на 100000 запросов:
```bash
$ ./waap probe --probe-num-requests 100000
$ ./waap export-results
```
2) Результаты будут лежать в директории `results`.

## Замеры
1М запросов (32GB оперативной памяти, swap 8GB, 16 ядер с поддержкой SIMD):
```bash
Processed a total of 699 http corpus streams
Processed a total of 1114166 http requests
Total time taken in minutes: 25.6629872598
AVG RPS: 723.588087822283
```
1) Wall-clock: 25.6629872598 минут
2) Средний RPS: 723.588087822283
4) Потребление памяти в среднем, учитывая другие процессы, было примерно 10GB (в пике до 15GB).
5) Потребление CPU держалось стабильно около 80% и ниже.
6) Экспортирование около 6 миллионов резульатов полного прогона из BadgerDB в json файлы заняло около 7 минут.
7) Линейность по ядрам проверялась на 100 тыс. запросов (использовались cgroups v2):
  - **1 core:**<br />
    Processed a total of 56 http corpus streams<br />
    Processed a total of 100000 http requests<br />
    Total time taken in minutes: 7.570689714<br />
    AVG RPS: 220.14492797961654<br />

  - **2 cores:**<br />
    Processed a total of 55 http corpus streams<br />
    Processed a total of 100000 http requests<br />
    Total time taken in minutes: 6.094748149716667<br />
    AVG RPS: 273.45941250803247<br />
    По сравнению с запуском на 1м ядре прогон на 2х ядрах был быстрее на 19.4%.<br />
    По RPS произошло улучшение на 24.2%.

  - **3 cores:**<br />
    Processed a total of 55 http corpus streams<br />
    Processed a total of 100000 http requests<br />
    Total time taken in minutes: 3.871145733716667<br />
    AVG RPS: 430.5355464209988<br />
    По сравнению с запуском на 2х ядрах прогон на 3х ядрах был быстрее на 36.4%.<br />
    По RPS произошло улучшение на 57.4%.

  - **4 cores:**<br />
    Processed a total of 55 http corpus streams<br />
    Processed a total of 100000 http requests<br />
    Total time taken in minutes: 5.35901492025<br />
    AVG RPS: 311.00233124710826<br />
    По сравнению с запуском на 3х ядрах прогон на 4х ядрах был `медленне` на 38.4%.<br />
    По RPS произошло `ухудшение` на 27.7%.

  - **5 cores:**<br />
    Processed a total of 55 http corpus streams<br />
    Processed a total of 100000 http requests<br />
    Total time taken in minutes: 2.7046115983<br />
    AVG RPS: 616.2310616184773<br />
    По сравнению с запуском на 4х ядрах прогон на 5и ядрах был быстрее на 49.5%.<br />
    По RPS произошло улучшение на 98.1%.

  - **6 cores:**<br />
    Processed a total of 53 http corpus streams<br />
    Processed a total of 100000 http requests<br />
    Total time taken in minutes: 2.5629805797333334<br />
    AVG RPS: 650.284088472384<br />
    По сравнению с запуском на 5и ядрах прогон на 6и ядрах был быстрее на 5.2%.<br />
    По RPS произошло улучшение на 5.5%.

  - **7 cores:**<br />
    Processed a total of 53 http corpus streams<br />
    Processed a total of 100000 http requests<br />
    Total time taken in minutes: 2.7861710732000002<br />
    AVG RPS: 598.1916951613024<br />
    По сравнению с запуском на 6и ядрах прогон на 7и ядрах был `медленне` на 8.7%.<br />
    По RPS произошло `ухудшение` на 8%.

  - **8 cores:**<br />
    Processed a total of 53 http corpus streams<br />
    Processed a total of 100000 http requests<br />
    Total time taken in minutes: 2.3307735334166666<br />
    AVG RPS: 715.0695024065592<br />
    По сравнению с запуском на 7и ядрах прогон на 8и ядрах был быстрее на 16.3%.<br />
    По RPS произошло улучшение на 19.5%.

  - **9 cores:**<br />
    Processed a total of 53 http corpus streams<br />
    Processed a total of 100000 http requests<br />
    Total time taken in minutes: 2.336088597233333<br />
    AVG RPS: 713.442742171303<br />
    По сравнению с запуском на 8 ядрах прогон на 9и ядрах был `медленне` на 0.2%.<br />
    По RPS произошло `ухудшение` на 0.2%.

  - **10 cores:**<br />
    Processed a total of 52 http corpus streams<br />
    Processed a total of 100000 http requests<br />
    Total time taken in minutes: 2.38286581695<br />
    AVG RPS: 699.4373789757012<br />
    По сравнению с запуском на 9и ядрах прогон на 10и ядрах был `медленне` на 2%.<br />
    По RPS произошло `ухудшение` на 1.9%.

  - **11 cores:**<br />
    Processed a total of 52 http corpus streams<br />
    Processed a total of 100000 http requests<br />
    Total time taken in minutes: 2.7554316509<br />
    AVG RPS: 604.8653720478992<br />
    По сравнению с запуском на 10и ядрах прогон на 11и ядрах был `медленне` на 15.6%.<br />
    По RPS произошло `ухудшение` на 13.5%.


  - **12 cores:**<br />
    Processed a total of 51 http corpus streams<br />
    Processed a total of 100000 http requests<br />
    Total time taken in minutes: 2.2822629673666666<br />
    AVG RPS: 730.2684901788554<br />
    По сравнению с запуском на 11и ядрах прогон на 11и ядрах был быстрее на 17.1%.<br />
    По RPS произошло улучшение на 20.7%.


  - **13 cores:**<br />
    Processed a total of 51 http corpus streams<br />
    Processed a total of 100000 http requests<br />
    Total time taken in minutes: 2.7970703458833333<br />
    AVG RPS: 595.8611117943468<br />
    По сравнению с запуском на 12и ядрах прогон на 13и ядрах был `медленне` на 19.4%.<br />
    По RPS произошло `ухудшение` на 24.2%.


  - **14 cores:**<br />
    Processed a total of 52 http corpus streams<br />
    Processed a total of 100000 http requests<br />
    Total time taken in minutes: 2.6582724435833334<br />
    AVG RPS: 626.9729603808897<br />
    По сравнению с запуском на 13и ядрах прогон на 14и ядрах был быстрее на 16.4%.<br />
    По RPS произошло улучшение на 24.2%.

  - **15 cores:**<br />
    Processed a total of 53 http corpus streams<br />
    Processed a total of 100000 http requests<br />
    Total time taken in minutes: 2.3156112267<br />
    AVG RPS: 719.7518110013635<br />
    По сравнению с запуском на 14и ядрах прогон на 15и ядрах был быстрее на 12.8.<br />
    По RPS произошло улучшение на 14.7%.


  - **16 cores:**<br />
    Processed a total of 51 http corpus streams<br />
    Processed a total of 100000 http requests<br />
    Total time taken in minutes: 2.7506837756999998<br />
    AVG RPS: 605.9094773939978<br />
    По сравнению с запуском на 16и ядрах прогон на 15и ядрах был `медленне` на 18.7%.<br />
    По RPS произошло `ухудшение` на 15.8%.
<br />
<br />
Лучший прирост по сравнению с прогоном на одном ядре показал прогон на 12и ядрах:
- Быстрее по времени прогона на 69.8%
- Лучше по RPS на 231.7%

## Baseline
На одном ядре прогон 100 тыс. запросов:<br />
Processed a total of 56 http corpus streams<br />
Processed a total of 100000 http requests<br />
Total time taken in minutes: 7.570689714<br />
AVG RPS: 220.14492797961654<br />


## Какие варианты пробовал
1) Первое во что я уперся при реализации было парсинг json стримов.<br />
Для начала я хотел использовать алгоритм matching-braces с использованием стэка.<br />
```go
func jsonStreamSplitFunc(data []byte, atEOF bool) (advance int, token []byte, err error) {
	stack := bracketsStackPool.Get().([]byte)
	defer func() {
		stack = stack[:0]
		bracketsStackPool.Put(stack)
	}()

	firstOpeningBracketIdx := -1
	for i := range data {
		switch data[i] {
		case '{':
			stack = append(stack, data[i])
			if firstOpeningBracketIdx == -1 {
				firstOpeningBracketIdx = i
			}
		case '}':
			stack = stack[:len(stack)-1]
		}
		if firstOpeningBracketIdx != -1 && len(stack) == 0 {
			return i + 2, data[firstOpeningBracketIdx : i+1], nil
		}
	}
	return 0, nil, nil
}
```
НО в теле и заголовках самих запросов могли быть рандомные скобки, поэтому этот вариант пришлось отбросить.

2) Второе - уперся в память, используя `easyjson` и `regexp2`. Заменил их на `protobuf` и `go-pcre`<br />
   CGO wrapper вокруг Сишной библиотеки `libpcre`

## Где упёрся (CPU? memory bandwidth...)
**Уперся в память при использовании `easyjson` и `regexp2`**<br />
Проблема, если посмотреть на pprof пройфайлы в папке `./ppro_profiles` крылась<br />
в библиотеке `regexp2`, которая на больших http запросах потребляла огромное кол-во памяти.<br />
<br />
Решение - заменить `regexp2` на `go-pcre` CGO wrapper вокруг Сишной библиотеки `libpcre`.<br />

## Как поступил с паттернами, несовместимыми с выбранным движком
При возникновении ошибки "quantifier does not follow a repeatable item" заэскейпил nested repetion regex операторы.

## Что бы делал на 100M / 1B и почему текущее решение туда без изменений не масштабируется
На 100M / 1B потребудется уже горизонтальное масшатбирование:
   - Сервис номер 1 парсит http корпусы и отправляет raw json-ы через голые TCP сокеты<br />
     в сервисы, которые занимаются nmap probing-ом (сервисов должно быть несколько для<br />
     fault tolerance).
   - Сервисы, которые занимаются nmap probing-ом разместил бы на разных машинах. После<br />
     обработки запроса и построение protobuf результата, они бы отправляли по gRPC эти<br />
     результаты в сервис который бы занимался сохранением результатов.
   - Принимающий gRPC сервис превращал бы protobuf в json или специализированные данные для БД<br />
     и сохранял бы результаты в LSM-Tree based БД для большего write-throuput (тоже должно<br />
     быть несколько инстансов таких сервисов).
   - Тут уже бы можно было бы использовать tokio, так как речь бы шла о чтении по сети.


## Обзор на текущую архитектуру приложения (пакет pkg/waap):
1) `waap.go::Waap` - является оркестратрором всего процесса.
2) Первым стартуер `nmap.go::nmapParser`, который лениво читает в буфер<br />
   и парсит nmap-service-probes превращая в массив regex парсеров, которые<br />
   учитывают разделения match / softmatch и rarity probe-ов, а так же наименования<br />
   сервисов и наименования самих probe-ов. Где возможно использует zero-allocation<br />
   конвертацию между []byte и string и обратно.
3) Далее `сorpuses.go::corpusStreamer`, который в фоне проходит по файлам<br />
   с http корпусами, отправляя их на дальнейшую обработку.
4) Далее эстафету принимают фоновые воркеры `streamer.go::requestStreamer`<br />
   (кол-во вокеров равняется GOMAXPROCS), которые принимают http корпус<br />
   стримы и лениво парсят их переиспользуя буферы, zero-allocation конвертацию между <br />
   []byte и string и обратно. Из каждого json объекта строят валидный http запрос<br />
   и отправляет дальше на обработку воркерам `prober.go::nmapProber`.
5) Фоновые воркеры `prober.go::nmapProber` принимают http запросы и матчат их с<br />
   nmap probe-ми, после чего строят json объекты, которые учитывают разделения<br />
   match / softmatch и rarity probe-ов, а так же наименования сервисов и наименования<br />
   самих probe-ов. Каждые 100 protobuf bytes объектов флашатся во встроенную BadgerDB.
6) Получение результатов: `waap export-results` проходится по всем данным в BadgerDB<br />
   и флашит их пачками по 1000 json объектов в json файлы.

## Что не успел добавить
1) Тесты на все компненты
2) Комментарии по коду