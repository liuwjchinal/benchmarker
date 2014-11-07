# Mono Benchmarker

## Configuring

Each Mono configuration requires a `.conf` file.  The files in the `configs` directory are examples.  The `$DIR` variable points to the benchmarker root directory.  Neither the results directories nor the mono executable need to be in subdirectories, like they are in the example configs.

## Benchmarking

To run the suite for a specific revision, use the `runner.sh` script. It must be run in the benchmarker root directory:

    ./runner.sh [-c <commit-sha1>] <revision> <config-file> ...

The revision can be an arbitrary string, but revision strings must be string-comparedly ascending.  [This blog post](http://blog.marcingil.com/2011/11/creating-build-numbers-using-git-commits/) describes a method for deriving such a revision string from git commits.  We would have to use more than four digits for the commit counter, of course.  If the SHA1 is available, pass it on.  It is used by the collect script for user-friendliness.

The script will place the result files in the directories `$RESULTS_DIR/$CONFIG_NAME/r$REVISION`.

## Producing running graphs

To collect benchmarking results from all configurations and revisions, use `collect.pl`, like so:

    ./collect.pl [--conf <config-file> ...] <root-dir> <config-subdir> ...

Where each of the `config-subdir` is a subdirectory of `root-dir`.  Typically `root-dir` would be `$RESULTS_DIR` and `config-subdir` would be `$CONFIG_NAME` from the configuration files.

You can specify any number of `config-file`s, using the `--conf` option.  Config files can specify revisions to ignore in the resulting output.

The script will generate in `index.html` in `root-dir` and further HTML and image files in the subdirectories.  Note that each of the individual original result files is linked to, so the whole `root-dir` tree is necessary for viewing, not just the files generated by `collect.pl`.

## Comparing directly

To compare two or more revisions and/or configurations directly, use `compare.py`:

    ./compare.py [--output <image-file>] <revision-dir> <revision-dir> ...

Where each `revision-dir` is a directory containing the `.times` files generated by `runner.sh`.  If an `image-file` is given, the graph is written to that file, otherwise it is displayed on the screen.

## Comparing counters

To compare counters for two or more revisions and/or configurations, you first need to run a benchmark with the log profiler enabled for each of the revision and/or configuration you want to test:

    mono --profile=log:nocalls,noalloc,counters,output=<proflog-out> benchmark.exe benchmark-args

Then use comparator/compare.exe and gnuplot to produce graphs:

    (cd comparator && mono compare.exe [--help] [-s <sections>] [-n <names>] [-c <columns>]
                                       [-h <height>] [-w <width>] <proflog-out>
									   [<proflog-out> ...] | gnuplot > graph.png)

Only Mono 3.8.0 and higher log profiler support counters sampling. If your installed Mono version is lower, you can still load a specific version of the log profiler by specifying `LD_LIBRARY_PATH="/path/to/mono-master/mono/profiler/.libs"`.

The output generated by the `compare.exe` tool is a gnuplot script, which means you need to install gnuplot first (`brew install gnuplot` on OSX).

## JSON results format

The new JSON results format is as follow :

    - DateTime : date and time at which this benchmark was run
    - Benchmark : copy data of the `benchmarks/*.benchmark` corresponding file
      - Name : name of the benchmark
      - TestDirectory : working directory to use to run the benchmark, relative to tests/
      - CommandLine : command line parameters to pass to the benchmark
    - Config : copy data of the `configs/*.conf` corresponding file
      - Name : name of the config
      - Count : number of time to run the benchmark
      - Mono : path to the mono executable
      - MonoOptions : command line parameters to pass to the mono runtime
      - MonoEnvironmentVariables : environment variables to set to run the benchmark
      - ResultsDirectory : path to the results directory, relative to the benchmarker repository root directory
    - Version : standard output when run with `--version` runtime command line parameter
    - Runs : collections of the runs for the benchnark, size is equal to Config.Count
      - WallClockTime : wall clock time taken to run the benchmark
      - Output : standard output of the benchmark
      - Error : standard error of the benchmark
    - Timedout : true if any of the run of the benchmark has timed out
