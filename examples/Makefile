TIM ?= ../tim
PROGRAMS := $(basename $(filter-out add.tim,$(wildcard *.tim)))

.PHONY: all clean

all: $(PROGRAMS)

%: %.tim
	$(TIM) -o $@ $<

hello: add.tim

clean:
	rm -f $(PROGRAMS)
