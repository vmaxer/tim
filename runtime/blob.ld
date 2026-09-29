SECTIONS {
	. = 0;
	.text : { *(.text .text.*) *(.rodata .rodata.*) *(.srodata .srodata.*) }
	/DISCARD/ : { *(.comment) *(.note .note.*) *(.eh_frame*) *(.riscv.attributes) }
}
