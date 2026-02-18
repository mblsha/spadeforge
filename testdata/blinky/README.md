## blinky testdata

Minimal Vivado-targeted design used for real FPGA integration checks.

- `top.sv`: 24-bit counter driving `led`
- `top.xdc`: Basys 3 pin constraints (`xc7a35tcpg236-1`)

This variant keeps real I/O ports so synthesis/place/route does not
collapse into an empty design.
