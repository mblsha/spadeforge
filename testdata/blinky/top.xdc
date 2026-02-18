## Clock - 100 MHz (Basys 3 W5)
set_property -dict {PACKAGE_PIN W5 IOSTANDARD LVCMOS33} [get_ports clk]
create_clock -period 10.000 -name sys_clk [get_ports clk]

## Reset - center button (U18)
set_property -dict {PACKAGE_PIN U18 IOSTANDARD LVCMOS33} [get_ports rst]

## LED0 (U16)
set_property -dict {PACKAGE_PIN U16 IOSTANDARD LVCMOS33} [get_ports led]
