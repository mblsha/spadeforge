module top (
    input  wire clk,
    input  wire rst,
    output wire led
);
    localparam integer COUNTER_BITS = 24;

    reg [COUNTER_BITS-1:0] counter;

    always @(posedge clk) begin
        if (rst)
            counter <= {COUNTER_BITS{1'b0}};
        else
            counter <= counter + 1'b1;
    end

    assign led = counter[COUNTER_BITS-1];
endmodule
