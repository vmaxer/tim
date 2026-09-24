SECTIONS {
	. = 0;
	.text : { *(.text.tim_num) *(.text .text.*) *(.rodata .rodata.*) }
	/DISCARD/ : { *(.comment) *(.note .note.*) *(.eh_frame*) }
}
